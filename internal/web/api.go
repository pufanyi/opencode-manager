package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pufanyi/opencode-manager/internal/provider"
)

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

	// Wrap with Firebase streaming if configured.
	ch = s.procMgr.WrapEventsIfFirebase(req.SessionID, ch)

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
