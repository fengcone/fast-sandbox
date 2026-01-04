package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"fast-sandbox/internal/agent/runtime"
	"fast-sandbox/internal/api"
)

// AgentServer handles HTTP requests from controller.
type AgentServer struct {
	addr           string
	sandboxManager *runtime.SandboxManager
}

// NewAgentServer creates a new agent HTTP server.
func NewAgentServer(addr string, sandboxManager *runtime.SandboxManager) *AgentServer {
	return &AgentServer{
		addr:           addr,
		sandboxManager: sandboxManager,
	}
}

// Start starts the HTTP server.
func (s *AgentServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/sync", s.handleSync)
	mux.HandleFunc("/api/v1/agent/status", s.handleStatus)

	log.Printf("Starting agent HTTP server on %s\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

func (s *AgentServer) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.SandboxesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.sandboxManager.SyncSandboxes(r.Context(), req.SandboxSpecs); err != nil {
		log.Printf("Sync failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *AgentServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Get Sandboxes
	sandboxes, err := s.sandboxManager.ListSandboxes(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 2. Get Images
	images, err := s.sandboxManager.ListImages(r.Context())
	if err != nil {
		log.Printf("Warning: failed to list images: %v", err)
		// Don't fail the whole request, just return empty images
		images = []string{}
	}

	// Convert runtime metadata to api status
	var sbStatuses []api.SandboxStatus
	for _, sb := range sandboxes {
		sbStatuses = append(sbStatuses, api.SandboxStatus{
			SandboxID: sb.SandboxID,
			ClaimUID:  sb.ClaimUID,
			Phase:     sb.Status,
			// Message: ...,
		})
	}
    
    // Node Name from Env
    nodeName := os.Getenv("NODE_NAME")

	status := api.AgentStatus{
		AgentID:        os.Getenv("POD_NAME"), // Use Pod Name as Agent ID
        NodeName:       nodeName,
		Capacity:       s.sandboxManager.GetCapacity(),
		Allocated:      len(sandboxes),
		Images:         images,
		SandboxStatuses: sbStatuses,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}