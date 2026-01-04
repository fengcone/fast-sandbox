package server

import (
	"log"
	"net/http"

	"fast-sandbox/internal/agent/runtime"
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
}
