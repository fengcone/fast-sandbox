package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

// AdmissionReview 标准的 Kubernetes AdmissionReview 结构
type AdmissionReview struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	Request    *AdmissionRequest  `json:"request,omitempty"`
	Response   *AdmissionResponse `json:"response,omitempty"`
}

// AdmissionRequest 准入请求
type AdmissionRequest struct {
	UID             string          `json:"uid"`
	Kind            Kind            `json:"kind"`
	Resource        Resource        `json:"resource"`
	SubResource     string          `json:"subResource,omitempty"`
	RequestKind     *Kind           `json:"requestKind,omitempty"`
	RequestResource *Resource       `json:"requestResource,omitempty"`
	Name            string          `json:"name"`
	Namespace       string          `json:"namespace"`
	Operation       string          `json:"operation"`
	UserInfo        UserInfo        `json:"userInfo"`
	Object          json.RawMessage `json:"object,omitempty"`
	OldObject       json.RawMessage `json:"oldObject,omitempty"`
	DryRun          bool            `json:"dryRun"`
	Options         json.RawMessage `json:"options,omitempty"`
}

type Kind struct {
	Kind string `json:"kind"`
}

type Resource struct {
	Group    string `json:"group"`
	Version  string `json:"version"`
	Resource string `json:"resource"`
}

type UserInfo struct {
	Username string `json:"username"`
	UID      string `json:"uid"`
}

// AdmissionResponse 准入响应
type AdmissionResponse struct {
	UID     string           `json:"uid"`
	Allowed bool             `json:"allowed"`
	Result  *AdmissionResult `json:"result,omitempty"`
}

type AdmissionResult struct {
	Message string `json:"message,omitempty"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "443"
	}

	rejectPattern := os.Getenv("REJECT_PATTERN")
	if rejectPattern == "" {
		rejectPattern = "test-orphan-"
	}

	http.HandleFunc("/validate", func(w http.ResponseWriter, r *http.Request) {
		handleAdmission(w, r, rejectPattern)
	})

	log.Printf("Starting webhook server on port %s, rejecting pattern: %s", port, rejectPattern)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func handleAdmission(w http.ResponseWriter, r *http.Request, rejectPattern string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read body: %v", err), http.StatusBadRequest)
		return
	}

	// 解析 AdmissionReview
	var review AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil {
		http.Error(w, fmt.Sprintf("failed to parse AdmissionReview: %v", err), http.StatusBadRequest)
		return
	}

	// 准备响应
	response := &AdmissionResponse{
		UID:     review.Request.UID,
		Allowed: true,
	}

	// 只处理 CREATE 操作
	if review.Request.Operation == "create" {
		name := review.Request.Name
		// 检查名称是否匹配拒绝模式
		if strings.HasPrefix(name, rejectPattern) {
			response.Allowed = false
			response.Result = &AdmissionResult{
				Message: fmt.Sprintf("Sandbox name '%s' matches reject pattern '%s' for orphan cleanup testing", name, rejectPattern),
			}
			log.Printf("Rejected CREATE for %s (matches pattern %s)", name, rejectPattern)
		} else {
			log.Printf("Allowed CREATE for %s (does not match pattern %s)", name, rejectPattern)
		}
	}

	// 构造响应 AdmissionReview
	respReview := AdmissionReview{
		APIVersion: "admission.k8s.io/v1",
		Kind:       "AdmissionReview",
		Response:   response,
	}

	respBody, err := json.Marshal(respReview)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to marshal response: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(respBody)
}
