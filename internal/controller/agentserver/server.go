package agentserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"
)

// Server handles HTTP requests from agents.
type Server struct {
	registry agentpool.AgentRegistry
	addr     string
}

// NewServer creates a new agent HTTP server.
func NewServer(registry agentpool.AgentRegistry, addr string) *Server {
	return &Server{
		registry: registry,
		addr:     addr,
	}
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/register", s.handleRegister)
	mux.HandleFunc("/api/v1/agent/heartbeat", s.handleHeartbeat)

	fmt.Printf("Starting agent HTTP server on %s\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

// handleRegister handles agent registration requests.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	// Register agent in memory
	info := agentpool.AgentInfo{
		ID:            agentpool.AgentID(req.AgentID),
		Namespace:     req.Namespace,
		PodName:       req.PodName,
		PodIP:         req.PodIP,
		NodeName:      req.NodeName,
		Capacity:      req.Capacity,
		Allocated:     0,
		Images:        req.Images,
		LastHeartbeat: time.Now(),
	}
	s.registry.RegisterOrUpdate(info)

	resp := api.RegisterResponse{
		Success: true,
		Message: "Agent registered successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleHeartbeat handles agent heartbeat requests.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	// Update agent heartbeat and running sandbox count
	agentID := agentpool.AgentID(req.AgentID)
	agent, ok := s.registry.GetAgentByID(agentID)
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	// Update allocated count based on running sandbox count
	agent.Allocated = req.RunningSandboxCount
	agent.LastHeartbeat = time.Now()
	if req.Images != nil {
		agent.Images = req.Images
	}
	s.registry.RegisterOrUpdate(agent)

	resp := api.HeartbeatResponse{
		Success: true,
		Message: "Heartbeat received",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
