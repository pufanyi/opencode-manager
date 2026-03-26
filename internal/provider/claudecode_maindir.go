package provider

import "log/slog"

// --- MainDirLocker implementation ---

func (p *ClaudeCodeProvider) IsMainDirBusy(sessionID string) bool {
	p.mainDirMu.Lock()
	defer p.mainDirMu.Unlock()
	return p.mainDirHolder != "" && p.mainDirHolder != sessionID
}

func (p *ClaudeCodeProvider) TryAcquireMainDir(sessionID string) bool {
	p.mainDirMu.Lock()
	defer p.mainDirMu.Unlock()
	if p.mainDirHolder == "" || p.mainDirHolder == sessionID {
		p.mainDirHolder = sessionID
		return true
	}
	return false
}

func (p *ClaudeCodeProvider) WaitMainDirFree() <-chan struct{} {
	p.mainDirMu.Lock()
	defer p.mainDirMu.Unlock()
	if p.mainDirHolder == "" {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	ch := make(chan struct{})
	p.mainDirNotify = append(p.mainDirNotify, ch)
	return ch
}

func (p *ClaudeCodeProvider) ReleaseMainDir(sessionID string) {
	p.mainDirMu.Lock()
	if p.mainDirHolder != sessionID {
		p.mainDirMu.Unlock()
		return
	}
	p.mainDirHolder = ""
	waiters := p.mainDirNotify
	p.mainDirNotify = nil
	p.mainDirMu.Unlock()

	for _, ch := range waiters {
		close(ch)
	}
	slog.Info("released main-dir lock", "session", sessionID)
}
