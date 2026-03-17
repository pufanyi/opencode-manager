package gitops

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// MergeResult describes the outcome of a merge-back attempt.
type MergeResult struct {
	Merged      bool
	Branch      string
	MainBranch  string
	CommitCount int
	Message     string
}

// MergeBack merges the current worktree branch into the main branch.
// It only operates if the directory is a linked git worktree with commits ahead of main.
func MergeBack(dir string) *MergeResult {
	// Check if this is a git repo at all
	if _, err := gitOutput(dir, "rev-parse", "--git-dir"); err != nil {
		slog.Debug("merge-back: not a git repo", "dir", dir)
		return &MergeResult{Message: "not a git repo"}
	}

	// Get current branch
	branch, err := gitOutput(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch == "HEAD" {
		return &MergeResult{Message: "detached HEAD, skipping merge"}
	}

	// Detect main branch name
	mainBranch := detectMainBranch(dir)
	if mainBranch == "" {
		return &MergeResult{Branch: branch, Message: "could not detect main branch (main/master)"}
	}

	// Already on main — nothing to merge
	if branch == mainBranch {
		return &MergeResult{Branch: branch, MainBranch: mainBranch, Message: "already on main branch"}
	}

	// Check for uncommitted changes
	status, _ := gitOutput(dir, "status", "--porcelain")
	if status != "" {
		return &MergeResult{
			Branch:     branch,
			MainBranch: mainBranch,
			Message:    "uncommitted changes exist, skipping merge",
		}
	}

	// Check if there are commits to merge
	logOut, err := gitOutput(dir, "log", mainBranch+"..HEAD", "--oneline")
	if err != nil || logOut == "" {
		return &MergeResult{
			Branch:     branch,
			MainBranch: mainBranch,
			Message:    "no new commits to merge",
		}
	}

	commitCount := len(strings.Split(strings.TrimSpace(logOut), "\n"))

	// Try to find the main worktree for a linked worktree
	mainDir, err := findMainWorktree(dir)
	if err != nil {
		// Not a linked worktree — try to update main ref directly via fast-forward
		slog.Debug("merge-back: not a linked worktree, trying fast-forward update", "dir", dir)
		return fastForwardRef(dir, branch, mainBranch, commitCount)
	}

	// Merge from the main worktree
	mergeOut, err := gitOutputCombined(mainDir, "merge", branch, "--no-edit")
	if err != nil {
		// Abort the failed merge
		_ = gitRun(mainDir, "merge", "--abort")
		slog.Warn("merge-back failed", "dir", dir, "branch", branch, "error", mergeOut)
		return &MergeResult{
			Branch:     branch,
			MainBranch: mainBranch,
			Message:    fmt.Sprintf("merge conflict, aborted: %s", firstLine(mergeOut)),
		}
	}

	slog.Info("merge-back succeeded", "dir", dir, "branch", branch, "main", mainBranch, "commits", commitCount)
	return &MergeResult{
		Merged:      true,
		Branch:      branch,
		MainBranch:  mainBranch,
		CommitCount: commitCount,
		Message:     fmt.Sprintf("merged %d commit(s) from %s into %s", commitCount, branch, mainBranch),
	}
}

// fastForwardRef updates the main branch ref to match the current branch if it's a fast-forward.
func fastForwardRef(dir, branch, mainBranch string, commitCount int) *MergeResult {
	// Check if main is an ancestor of HEAD (fast-forward possible)
	if err := gitRun(dir, "merge-base", "--is-ancestor", mainBranch, "HEAD"); err != nil {
		return &MergeResult{
			Branch:     branch,
			MainBranch: mainBranch,
			Message:    "branches have diverged, cannot fast-forward",
		}
	}

	// Update main branch ref to current HEAD
	if _, err := gitOutput(dir, "branch", "-f", mainBranch, "HEAD"); err != nil {
		return &MergeResult{
			Branch:     branch,
			MainBranch: mainBranch,
			Message:    fmt.Sprintf("failed to update %s ref: %s", mainBranch, err),
		}
	}

	slog.Info("merge-back: fast-forward succeeded", "dir", dir, "branch", branch, "main", mainBranch)
	return &MergeResult{
		Merged:      true,
		Branch:      branch,
		MainBranch:  mainBranch,
		CommitCount: commitCount,
		Message:     fmt.Sprintf("fast-forwarded %s to %s (%d commit(s))", mainBranch, branch, commitCount),
	}
}

func detectMainBranch(dir string) string {
	for _, name := range []string{"main", "master"} {
		if err := gitRun(dir, "rev-parse", "--verify", name); err == nil {
			return name
		}
	}
	return ""
}

// findMainWorktree returns the path of the main (non-linked) worktree.
// Returns error if the current dir IS the main worktree or if parsing fails.
func findMainWorktree(dir string) (string, error) {
	output, err := gitOutput(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}

	toplevel, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}

	// Parse porcelain output: first "worktree <path>" is the main worktree
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			mainDir := strings.TrimPrefix(line, "worktree ")
			if mainDir == toplevel {
				// We ARE the main worktree
				return "", fmt.Errorf("current dir is the main worktree")
			}
			return mainDir, nil
		}
	}

	return "", fmt.Errorf("no worktree found in output")
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitOutputCombined(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
