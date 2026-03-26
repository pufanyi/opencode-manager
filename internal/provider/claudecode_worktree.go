package provider

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pufanyi/opencode-manager/internal/store"
)

// isGitRepo checks whether the instance directory is a git repository.
func (p *ClaudeCodeProvider) isGitRepo() bool {
	cmd := exec.Command("git", "-C", p.dir, "rev-parse", "--is-inside-work-tree")
	return cmd.Run() == nil
}

// currentBranch returns the current branch name in the main repo.
func (p *ClaudeCodeProvider) currentBranch() string {
	out, err := exec.Command("git", "-C", p.dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "main"
	}
	return strings.TrimSpace(string(out))
}

// createWorktree creates a git worktree for a session and returns (worktreePath, branchName, error).
func (p *ClaudeCodeProvider) createWorktree(sessionID string) (string, string, error) {
	branch := fmt.Sprintf("session/%s", sessionID[:12])
	wtPath := filepath.Join(os.TempDir(), "opencode-manager", p.instID, "worktrees", sessionID[:12])

	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir for worktree: %w", err)
	}

	// Create worktree with a new branch based on current HEAD
	cmd := exec.Command("git", "-C", p.dir, "worktree", "add", "-b", branch, wtPath, "HEAD")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("git worktree add: %s: %w", stderr.String(), err)
	}

	slog.Info("created git worktree", "session", sessionID[:12], "path", wtPath, "branch", branch)
	return wtPath, branch, nil
}

// removeWorktree cleans up a git worktree.
func (p *ClaudeCodeProvider) removeWorktree(wtPath string) error {
	// First try git worktree remove
	cmd := exec.Command("git", "-C", p.dir, "worktree", "remove", "--force", wtPath)
	if err := cmd.Run(); err != nil {
		slog.Warn("git worktree remove failed, cleaning up manually", "path", wtPath, "error", err)
		os.RemoveAll(wtPath)
	}

	// Prune stale worktrees
	_ = exec.Command("git", "-C", p.dir, "worktree", "prune").Run()
	return nil
}

// mergeAndSync merges a worktree branch back to the base branch and syncs other worktrees.
func (p *ClaudeCodeProvider) mergeAndSync(sessionID, wtPath, branch string) error {
	p.mergeMu.Lock()
	defer p.mergeMu.Unlock()

	baseBranch := p.currentBranch()

	// Check if the session branch has any new commits vs the base
	diffCmd := exec.Command("git", "-C", wtPath, "diff", "--quiet", baseBranch+".."+branch)
	if diffCmd.Run() == nil {
		slog.Info("no changes to merge", "session", sessionID[:12], "branch", branch)
		return nil
	}

	// Merge session branch into base branch in the main repo
	mergeCmd := exec.Command("git", "-C", p.dir, "merge", "--no-ff", "-m",
		fmt.Sprintf("Merge session/%s", sessionID[:12]), branch)
	var mergeStderr bytes.Buffer
	mergeCmd.Stderr = &mergeStderr
	if err := mergeCmd.Run(); err != nil {
		// Abort the failed merge
		_ = exec.Command("git", "-C", p.dir, "merge", "--abort").Run()
		return fmt.Errorf("merge failed (branch %s): %s: %w", branch, mergeStderr.String(), err)
	}

	slog.Info("merged session branch", "session", sessionID[:12], "branch", branch, "into", baseBranch)

	// Sync all other active worktrees by rebasing them onto updated base
	p.syncWorktrees(sessionID, baseBranch)

	// Delete the merged branch
	_ = exec.Command("git", "-C", p.dir, "branch", "-d", branch).Run()

	return nil
}

// syncWorktrees rebases all active worktrees (except the one that just merged) onto the base branch.
func (p *ClaudeCodeProvider) syncWorktrees(excludeSessionID, baseBranch string) {
	sessions, err := p.store.ListClaudeSessions(p.instID)
	if err != nil {
		slog.Warn("failed to list sessions for sync", "error", err)
		return
	}

	for _, s := range sessions {
		if s.ID == excludeSessionID || s.WorktreePath == "" {
			continue
		}
		// Check the worktree still exists
		if _, err := os.Stat(s.WorktreePath); os.IsNotExist(err) {
			continue
		}

		// Rebase the session branch onto the updated base
		rebaseCmd := exec.Command("git", "-C", s.WorktreePath, "rebase", baseBranch)
		var stderr bytes.Buffer
		rebaseCmd.Stderr = &stderr
		if err := rebaseCmd.Run(); err != nil {
			slog.Warn("rebase failed for worktree, aborting rebase",
				"session", s.ID[:12], "error", stderr.String())
			_ = exec.Command("git", "-C", s.WorktreePath, "rebase", "--abort").Run()
		} else {
			slog.Info("synced worktree", "session", s.ID[:12])
		}
	}
}

// enforceWorktreeLimit evicts the oldest non-active worktree sessions (FIFO)
// to keep the total count under maxWorktrees, making room for one new worktree.
func (p *ClaudeCodeProvider) enforceWorktreeLimit() {
	sessions, err := p.store.ListClaudeSessions(p.instID)
	if err != nil {
		slog.Warn("failed to list sessions for worktree limit", "error", err)
		return
	}

	// Collect sessions that have worktrees (ListClaudeSessions returns newest first)
	var wtSessions []store.ClaudeSession
	for _, s := range sessions {
		if s.WorktreePath != "" {
			wtSessions = append(wtSessions, s)
		}
	}

	// Need to make room for one new worktree
	if len(wtSessions) < p.maxWorktrees {
		return
	}
	evictCount := len(wtSessions) - p.maxWorktrees + 1

	// Snapshot active sessions to avoid evicting running ones
	p.mu.Lock()
	activeSet := make(map[string]bool, len(p.activeCmds))
	for sid := range p.activeCmds {
		activeSet[sid] = true
	}
	p.mu.Unlock()

	// Evict from oldest (end of slice) to newest
	evicted := 0
	for i := len(wtSessions) - 1; i >= 0 && evicted < evictCount; i-- {
		s := wtSessions[i]
		if activeSet[s.ID] {
			continue
		}

		slog.Info("evicting old worktree (FIFO)", "session", s.ID[:12], "branch", s.Branch)
		if s.WorktreePath != "" {
			_ = p.removeWorktree(s.WorktreePath)
		}
		if s.Branch != "" {
			_ = exec.Command("git", "-C", p.dir, "branch", "-D", s.Branch).Run()
		}
		_ = p.store.DeleteClaudeSession(p.instID, s.ID)
		evicted++
	}

	if evicted > 0 {
		slog.Info("worktree FIFO eviction done", "evicted", evicted, "limit", p.maxWorktrees)
	}
}

func (p *ClaudeCodeProvider) SupportsWorktree() bool {
	return p.isGitRepo()
}
