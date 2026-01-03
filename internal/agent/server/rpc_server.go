package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

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
	mux.HandleFunc("/api/v1/agent/sandboxes", s.handleSandboxes)

	log.Printf("Starting agent HTTP server on %s\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

// handleSandboxes handles desired/actual sandbox sync from controller.
func (s *AgentServer) handleSandboxes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.SandboxesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	// 使用 SandboxManager 同步期望的 sandbox 状态
	ctx := context.Background()
	statuses, err := s.sandboxManager.SyncSandboxes(ctx, req.Sandboxes)
	if err != nil {
		log.Printf("Failed to sync sandboxes: %v", err)
		http.Error(w, fmt.Sprintf("Failed to sync sandboxes: %v", err), http.StatusInternalServerError)
		return
	}

	// 构建响应
	images, err := s.sandboxManager.ListImages(ctx)
	if err != nil {
		log.Printf("Warning: failed to list images: %v", err)
		images = []string{} // 失败时返回空列表
	}

	resp := api.SandboxesResponse{
		AgentID:             req.AgentID,
		Capacity:            s.sandboxManager.GetCapacity(),
		RunningSandboxCount: s.sandboxManager.GetRunningSandboxCount(),
		Images:              images,
		Sandboxes:           statuses,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("failed to encode sandboxes response: %v", err)
	}
}
