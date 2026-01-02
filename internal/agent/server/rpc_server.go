package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"fast-sandbox/internal/api"
)

// AgentServer handles HTTP requests from controller.
type AgentServer struct {
	addr string
}

// NewAgentServer creates a new agent HTTP server.
func NewAgentServer(addr string) *AgentServer {
	return &AgentServer{
		addr: addr,
	}
}

// Start starts the HTTP server.
func (s *AgentServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/sandboxes", s.handleSandboxes)
	mux.HandleFunc("/api/v1/sandbox/create", s.handleCreateSandbox)
	mux.HandleFunc("/api/v1/sandbox/destroy", s.handleDestroySandbox)

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

	// TODO: 调用 SandboxManager，根据 req.Sandboxes 与当前状态对比，执行容器增删改。
	// 目前先使用 mock 实现：将所有期望 sandbox 标记为 Running。

	statuses := make([]api.SandboxStatus, 0, len(req.Sandboxes))
	for _, d := range req.Sandboxes {
		status := api.SandboxStatus{
			SandboxID: d.SandboxID,
			ClaimUID:  d.ClaimUID,
			Phase:     "Running",
			Port:      d.Port,
		}
		statuses = append(statuses, status)
	}

	resp := api.SandboxesResponse{
		AgentID:             req.AgentID,
		Capacity:            10,
		RunningSandboxCount: len(statuses),
		Sandboxes:           statuses,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("failed to encode sandboxes response: %v", err)
	}
}

// handleCreateSandbox handles sandbox creation requests from controller.
func (s *AgentServer) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.CreateSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	// TODO: Implement actual sandbox creation with containerd
	// For now, return a mock response
	sandboxID := fmt.Sprintf("sandbox-%s", req.ClaimUID[:8])
	port := req.Port
	if port == 0 {
		port = 8080
	}

	log.Printf("Creating sandbox for claim %s, image: %s\n", req.ClaimName, req.Image)

	resp := api.CreateSandboxResponse{
		Success:   true,
		SandboxID: sandboxID,
		Port:      port,
		Message:   "Sandbox created successfully (mock)",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleDestroySandbox handles sandbox destruction requests from controller.
func (s *AgentServer) handleDestroySandbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.DestroySandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	// TODO: Implement actual sandbox destruction with containerd
	log.Printf("Destroying sandbox %s\n", req.SandboxID)

	resp := api.DestroySandboxResponse{
		Success: true,
		Message: "Sandbox destroyed successfully (mock)",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
