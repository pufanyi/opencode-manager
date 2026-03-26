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
	"time"

	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/store"
)

//go:embed all:dist
var distFS embed.FS

type Server struct {
	procMgr *process.Manager
	store   store.Store
	addr    string
	server  *http.Server

	// WebSocket hub for streaming
	hub *StreamHub

	// Dev mode: reverse proxy to Angular dev server
	devProxy *DevProxy
}

func NewServer(addr string, procMgr *process.Manager, st store.Store) *Server {
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
