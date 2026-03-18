package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
)

//go:embed all:dist
var distFS embed.FS

type Server struct {
	procMgr *process.Manager
	store   *store.Store
	addr    string
	server  *http.Server

	// WebSocket hub for streaming
	hub *StreamHub

	// Dev mode: reverse proxy to Angular dev server
	devProxy *DevProxy
}

func NewServer(addr string, procMgr *process.Manager, st *store.Store) *Server {
	s := &Server{
		procMgr: procMgr,
		store:   st,
		addr:    addr,
		hub:     NewStreamHub(),
	}
	return s
}

// SetDevProxy configures the server to reverse-proxy non-API requests
// to an Angular dev server instead of serving embedded files.
func (s *Server) SetDevProxy(dp *DevProxy) {
	s.devProxy = dp
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/instances", s.handleInstances)
	mux.HandleFunc("/api/instances/", s.handleInstanceDetail)
	mux.HandleFunc("/api/sessions/", s.handleSessions)
	mux.HandleFunc("/api/prompt", s.handlePrompt)
	mux.HandleFunc("/api/abort", s.handleAbort)
	mux.HandleFunc("/api/ws", s.hub.HandleWebSocket)

	if s.devProxy != nil {
		// Dev mode: reverse proxy to Angular dev server (supports HMR/WebSocket)
		mux.Handle("/", s.devProxy)
	} else {
		// Production: serve embedded Angular build
		distContent, err := fs.Sub(distFS, "dist/browser")
		if err != nil {
			return fmt.Errorf("accessing embedded dist: %w", err)
		}
		fileServer := http.FileServer(http.FS(distContent))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// SPA fallback: serve index.html for non-file routes
			path := r.URL.Path
			if path != "/" && !strings.Contains(path, ".") {
				r.URL.Path = "/"
			}
			fileServer.ServeHTTP(w, r)
		})
	}

	s.server = &http.Server{
		Addr:    s.addr,
		Handler: corsMiddleware(mux),
	}

	go s.hub.Run(ctx)

	slog.Info("web dashboard starting", "addr", s.addr)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("web server error", "error", err)
		}
	}()

	return nil
}

func (s *Server) Stop() {
	if s.devProxy != nil {
		s.devProxy.Stop()
	}
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.server.Shutdown(ctx)
	}
}

// Hub returns the stream hub for broadcasting events from providers.
func (s *Server) Hub() *StreamHub {
	return s.hub
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// --- API Handlers ---

type instanceJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Directory    string `json:"directory"`
	Status       string `json:"status"`
	ProviderType string `json:"provider_type"`
	Port         int    `json:"port,omitempty"`
}

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		instances := s.procMgr.ListInstances()
		result := make([]instanceJSON, len(instances))
		for i, inst := range instances {
			result[i] = instanceJSON{
				ID:           inst.ID,
				Name:         inst.Name,
				Directory:    inst.Directory,
				Status:       string(inst.Status()),
				ProviderType: string(inst.ProviderType),
				Port:         inst.Port,
			}
		}
		writeJSON(w, result)

	case "POST":
		var req struct {
			Name      string `json:"name"`
			Directory string `json:"directory"`
			Provider  string `json:"provider"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid request body")
			return
		}
		provType := provider.Type(req.Provider)
		if provType == "" {
			provType = provider.TypeClaudeCode
		}
		inst, err := s.procMgr.CreateAndStart(req.Name, req.Directory, false, provType)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, instanceJSON{
			ID:           inst.ID,
			Name:         inst.Name,
			Directory:    inst.Directory,
			Status:       string(inst.Status()),
			ProviderType: string(inst.ProviderType),
		})

	default:
		writeError(w, 405, "method not allowed")
	}
}

func (s *Server) handleInstanceDetail(w http.ResponseWriter, r *http.Request) {
	// /api/instances/{id}/{action}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/instances/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, 400, "instance id required")
		return
	}
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	inst := s.procMgr.GetInstance(id)
	if inst == nil {
		writeError(w, 404, "instance not found")
		return
	}

	switch action {
	case "start":
		if r.Method != "POST" {
			writeError(w, 405, "method not allowed")
			return
		}
		if err := s.procMgr.StartInstance(id); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "started"})

	case "stop":
		if r.Method != "POST" {
			writeError(w, 405, "method not allowed")
			return
		}
		if err := s.procMgr.StopInstance(id); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "stopped"})

	case "delete":
		if r.Method != "DELETE" && r.Method != "POST" {
			writeError(w, 405, "method not allowed")
			return
		}
		if err := s.procMgr.DeleteInstance(id); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "deleted"})

	case "sessions":
		if r.Method != "GET" {
			writeError(w, 405, "method not allowed")
			return
		}
		sessions, err := inst.Provider.ListSessions(r.Context())
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, sessions)

	default:
		// GET instance detail
		if r.Method == "GET" {
			writeJSON(w, instanceJSON{
				ID:           inst.ID,
				Name:         inst.Name,
				Directory:    inst.Directory,
				Status:       string(inst.Status()),
				ProviderType: string(inst.ProviderType),
				Port:         inst.Port,
			})
		} else {
			writeError(w, 400, "unknown action")
		}
	}
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	// /api/sessions/{instanceId}/new
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/")
	if len(parts) == 0 {
		writeError(w, 400, "instance id required")
		return
	}
	instanceID := parts[0]
	inst := s.procMgr.GetInstance(instanceID)
	if inst == nil {
		writeError(w, 404, "instance not found")
		return
	}

	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	if action == "new" && r.Method == "POST" {
		session, err := inst.Provider.CreateSession(r.Context(), nil)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, session)
		return
	}

	writeError(w, 400, "unknown action")
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}

	var req struct {
		InstanceID string `json:"instance_id"`
		SessionID  string `json:"session_id"`
		Content    string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	inst := s.procMgr.GetInstance(req.InstanceID)
	if inst == nil {
		writeError(w, 404, "instance not found")
		return
	}

	ch, err := inst.Provider.Prompt(context.Background(), req.SessionID, req.Content)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	// Broadcast events to WebSocket clients
	go func() {
		for evt := range ch {
			s.hub.Broadcast(req.SessionID, evt)
		}
	}()

	writeJSON(w, map[string]string{"status": "started"})
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}

	var req struct {
		InstanceID string `json:"instance_id"`
		SessionID  string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	inst := s.procMgr.GetInstance(req.InstanceID)
	if inst == nil {
		writeError(w, 404, "instance not found")
		return
	}

	if err := inst.Provider.Abort(r.Context(), req.SessionID); err != nil {
		writeError(w, 500, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "aborted"})
}

// StreamHub manages WebSocket connections for real-time streaming.
type StreamHub struct {
	mu      sync.RWMutex
	clients map[*wsClient]bool
}

type wsClient struct {
	sessionFilter string // empty = all sessions
	send          chan []byte
}

func NewStreamHub() *StreamHub {
	return &StreamHub{
		clients: make(map[*wsClient]bool),
	}
}

func (h *StreamHub) Run(ctx context.Context) {
	<-ctx.Done()
	h.mu.Lock()
	for c := range h.clients {
		close(c.send)
	}
	h.clients = make(map[*wsClient]bool)
	h.mu.Unlock()
}

func (h *StreamHub) Broadcast(sessionID string, evt provider.StreamEvent) {
	data, _ := json.Marshal(map[string]any{
		"session_id": sessionID,
		"event":      evt,
	})

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.sessionFilter == "" || c.sessionFilter == sessionID {
			select {
			case c.send <- data:
			default:
				// Drop if client is slow
			}
		}
	}
}

// HandleWebSocket is a simple SSE-based streaming endpoint (no external WebSocket library needed).
// Clients connect to /api/ws?session=<id> for filtered events, or /api/ws for all.
func (h *StreamHub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming not supported")
		return
	}

	sessionFilter := r.URL.Query().Get("session")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	client := &wsClient{
		sessionFilter: sessionFilter,
		send:          make(chan []byte, 64),
	}

	h.mu.Lock()
	h.clients[client] = true
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
	}()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-client.send:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
