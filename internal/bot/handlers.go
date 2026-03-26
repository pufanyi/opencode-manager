package bot

import (
	"context"
	"fmt"
	"sync"

	"github.com/pufanyi/opencode-manager/internal/firebase"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/store"
)

// pendingPrompt stores a prompt waiting for the user's worktree/conflict choice.
type pendingPrompt struct {
	text         string // prompt content (empty for pure session creation)
	inst         *process.Instance
	userID       int64
	chatID       int64
	replyMsgID   int
	titleHint    string
	cleanupFiles []string // temp files to remove on discard
	choiceMsgID  int      // ID of the "where to work?" / conflict message
	sessionID    string   // non-empty when continuing an existing session
}

type Handlers struct {
	procMgr   *process.Manager
	store     store.Store
	streamMgr *StreamManager
	firebase  *firebase.Client
	tgState   *firebase.TelegramState

	pendingMu      sync.Mutex
	pendingPrompts map[int64]*pendingPrompt // userID -> pending
}

func NewHandlers(procMgr *process.Manager, st store.Store, streamMgr *StreamManager, tgState *firebase.TelegramState) *Handlers {
	return &Handlers{
		procMgr:        procMgr,
		store:          st,
		streamMgr:      streamMgr,
		tgState:        tgState,
		pendingPrompts: make(map[int64]*pendingPrompt),
	}
}

// getActiveInstance returns the active instance for a user.
func (h *Handlers) getActiveInstance(ctx context.Context, userID int64) (*process.Instance, error) {
	state, err := h.tgState.GetUserState(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user state: %s", err)
	}

	if state.ActiveInstanceID == "" {
		return nil, fmt.Errorf("no active instance. Use /list to select one")
	}

	inst := h.procMgr.GetInstance(state.ActiveInstanceID)
	if inst == nil {
		return nil, fmt.Errorf("active instance not found. Use /list to select another")
	}

	if inst.Status() != process.StatusRunning {
		return nil, fmt.Errorf("instance '%s' is not running. Use /start_inst %s", inst.Name, inst.Name)
	}

	return inst, nil
}
