package bot

import (
	"context"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/pufanyi/opencode-manager/internal/firebase"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
)

// StreamManager handles all active streams and global rate limiting.
type StreamManager struct {
	mu        sync.Mutex
	streams   map[string]*StreamContext // keyed by sessionID
	taskMap   map[int]*StreamContext    // keyed by taskID
	semaphore chan struct{}
	nextID    int

	// Status board
	b             *bot.Bot
	boardMu       sync.Mutex
	boardMsgs     map[int64]int    // chatID -> board message ID
	boardContent  map[int64]string // chatID -> last sent content
	boardRepos    map[int64]bool   // chatID -> needs reposition (new msg appeared)
	boardStarted  bool
	boardInterval time.Duration
}

func NewStreamManager(boardInterval time.Duration) *StreamManager {
	if boardInterval <= 0 {
		boardInterval = 2 * time.Second
	}
	return &StreamManager{
		streams:       make(map[string]*StreamContext),
		taskMap:       make(map[int]*StreamContext),
		semaphore:     make(chan struct{}, 25),
		boardMsgs:     make(map[int64]int),
		boardContent:  make(map[int64]string),
		boardRepos:    make(map[int64]bool),
		boardInterval: boardInterval,
	}
}

func (sm *StreamManager) StartStream(b *bot.Bot, st store.Store, tgState *firebase.TelegramState, chatID int64, instanceID, sessionID, instanceName, sessionTitle, workDir string, replyToMessageID int, ch <-chan provider.StreamEvent, promptCancel context.CancelFunc, abortFunc func()) *StreamContext {
	ctx, cancel := context.WithCancel(context.Background())

	sm.mu.Lock()
	sm.nextID++
	taskID := sm.nextID

	if old, ok := sm.streams[sessionID]; ok {
		old.MarkSuperseded()
		old.cancel()
		delete(sm.taskMap, old.taskID)
	}

	sc := &StreamContext{
		b:                b,
		store:            st,
		tgState:          tgState,
		chatID:           chatID,
		instanceID:       instanceID,
		sessionID:        sessionID,
		instanceName:     instanceName,
		sessionTitle:     sessionTitle,
		workDir:          workDir,
		replyToMessageID: replyToMessageID,
		startedAt:        time.Now(),
		manager:          sm,
		taskID:           taskID,
		cancel:           cancel,
		promptCancel:     promptCancel,
		abortFunc:        abortFunc,
	}

	// Look up session location for board display
	if st != nil {
		if cs, err := st.GetClaudeSession(instanceID, sessionID); err == nil && cs != nil {
			if cs.Branch != "" {
				sc.location = "🌿 worktree"
			} else {
				sc.location = "📂 main dir"
			}
		}
	}

	sm.streams[sessionID] = sc
	sm.taskMap[taskID] = sc

	if !sm.boardStarted {
		sm.b = b
		sm.boardStarted = true
		go sm.boardLoop()
	}
	sm.mu.Unlock()

	go sc.consumeStream(ctx, ch)
	go sc.flushLoop(ctx, sm.semaphore)

	// New message (user prompt) appeared — reposition board to bottom
	sm.NotifyNewMessage(chatID)
	go sm.refreshBoard()

	return sc
}

// RemoveStream cancels and removes a stream by sessionID (used by /abort).
func (sm *StreamManager) RemoveStream(sessionID string) {
	sm.mu.Lock()
	if sc, ok := sm.streams[sessionID]; ok {
		sc.cancel()
		if sc.promptCancel != nil {
			sc.promptCancel()
		}
		delete(sm.taskMap, sc.taskID)
		delete(sm.streams, sessionID)
	}
	sm.mu.Unlock()
	go sm.refreshBoard()
}

// StopTask cancels and removes a stream by taskID (used by board stop buttons).
func (sm *StreamManager) StopTask(taskID int) bool {
	sm.mu.Lock()
	sc, ok := sm.taskMap[taskID]
	if !ok {
		sm.mu.Unlock()
		return false
	}
	sc.cancel()
	promptCancel := sc.promptCancel
	abortFn := sc.abortFunc
	delete(sm.streams, sc.sessionID)
	delete(sm.taskMap, taskID)
	sm.mu.Unlock()

	if promptCancel != nil {
		promptCancel()
	}
	if abortFn != nil {
		abortFn()
	}
	go sm.refreshBoard()
	return true
}

// NotifyNewMessage marks a chat as needing the board repositioned to the bottom.
// Call this whenever a new message is sent to the chat (response, merge notification, etc.).
func (sm *StreamManager) NotifyNewMessage(chatID int64) {
	sm.boardMu.Lock()
	sm.boardRepos[chatID] = true
	sm.boardMu.Unlock()
}

// onStreamDone is called by flushLoop when a stream finishes naturally.
func (sm *StreamManager) onStreamDone(sessionID string) {
	sm.mu.Lock()
	if sc, ok := sm.streams[sessionID]; ok {
		delete(sm.taskMap, sc.taskID)
	}
	delete(sm.streams, sessionID)
	sm.mu.Unlock()
	go sm.refreshBoard()
}
