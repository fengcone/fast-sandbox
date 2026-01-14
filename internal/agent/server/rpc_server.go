package server

import (
	"encoding/json"
	"io"
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
	mux.HandleFunc("/api/v1/agent/create", s.handleCreate)
	mux.HandleFunc("/api/v1/agent/delete", s.handleDelete)
	mux.HandleFunc("/api/v1/agent/status", s.handleStatus)
	mux.HandleFunc("/api/v1/agent/logs", s.handleLogs)

	log.Printf("Starting agent HTTP server on %s\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

// handleLogs streams sandbox logs.
func (s *AgentServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sandboxID := r.URL.Query().Get("sandboxId")
	if sandboxID == "" {
		http.Error(w, "sandboxId is required", http.StatusBadRequest)
		return
	}
	follow := r.URL.Query().Get("follow") == "true"

	// 支持流式输出
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")

	// 使用 FlushWriter 包装 ResponseWriter，确保每写一行都 Flush
	fw := &flushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		fw.f = f
	}

	if err := s.sandboxManager.GetLogs(r.Context(), sandboxID, follow, fw); err != nil {
		// 如果已经写过 Header，这里的 Error 可能客户端收不到，只能记录日志
		log.Printf("GetLogs failed: %v", err)
		return
	}
}

type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (n int, err error) {
	n, err = fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return
}

// handleCreate handles create sandbox requests.
func (s *AgentServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.CreateSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp, err := s.sandboxManager.CreateSandbox(r.Context(), req.Sandbox)
	if err != nil {
		log.Printf("Create sandbox failed: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(resp)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleDelete handles delete sandbox requests.
func (s *AgentServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.DeleteSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp, err := s.sandboxManager.DeleteSandbox(r.Context(), req.SandboxID)
	if err != nil {
		log.Printf("Delete sandbox failed: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(resp)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleStatus handles status queries.
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
			CreatedAt: sb.CreatedAt, // Include creation time for orphan cleanup
		})
	}

	// Node Name from Env
	nodeName := os.Getenv("NODE_NAME")

	status := api.AgentStatus{
		AgentID:         os.Getenv("POD_NAME"), // Use Pod Name as Agent ID
		NodeName:        nodeName,
		Capacity:        s.sandboxManager.GetCapacity(),
		Allocated:       len(sandboxes),
		Images:          images,
		SandboxStatuses: sbStatuses,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}
