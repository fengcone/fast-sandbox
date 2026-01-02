package main

import (
	"log"
	"os"
	"time"

	"fast-sandbox/internal/agent/client"
	"fast-sandbox/internal/agent/server"
	"fast-sandbox/internal/api"
)

func main() {
	log.Println("starting sandbox agent")

	// 读取环境变量（在 Pod 中运行时由 Downward API 或环境变量提供）
	agentID := getEnv("AGENT_ID", "agent-local-test")
	podName := getEnv("POD_NAME", "test-agent-pod")
	podIP := getEnv("POD_IP", "127.0.0.1")
	nodeName := getEnv("NODE_NAME", "local-node")
	namespace := getEnv("NAMESPACE", "default")
	controllerURL := getEnv("CONTROLLER_URL", "http://localhost:9090")
	agentPort := getEnv("AGENT_PORT", ":8081")

	// 创建 Controller Client
	ctrlClient := client.NewControllerClient(controllerURL)

	// 注册到 Controller
	registerReq := &api.RegisterRequest{
		AgentID:   agentID,
		Namespace: namespace,
		PodName:   podName,
		PodIP:     podIP,
		NodeName:  nodeName,
		Capacity:  10,
		Images:    []string{"nginx:latest", "redis:latest", "ubuntu:22.04"},
	}

	log.Printf("Registering agent %s with controller at %s\n", agentID, controllerURL)
	regResp, err := ctrlClient.Register(registerReq)
	if err != nil {
		log.Fatalf("Failed to register: %v", err)
	}
	log.Printf("Registration successful: %s\n", regResp.Message)

	// 启动 HTTP Server 接收 Controller 的请求
	agentServer := server.NewAgentServer(agentPort)
	go func() {
		if err := agentServer.Start(); err != nil {
			log.Fatalf("Agent server failed: %v", err)
		}
	}()

	// 启动心跳协程
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			heartbeatReq := &api.HeartbeatRequest{
				AgentID:             agentID,
				RunningSandboxCount: 0, // TODO: 从 SandboxManager 获取实际数量
				Timestamp:           time.Now().Unix(),
			}

			_, err := ctrlClient.Heartbeat(heartbeatReq)
			if err != nil {
				log.Printf("Heartbeat failed: %v", err)
				// 如果心跳失败（可能是 Controller 重启），尝试重新注册
				log.Println("Attempting to re-register agent...")
				regResp, regErr := ctrlClient.Register(registerReq)
				if regErr != nil {
					log.Printf("Re-registration failed: %v", regErr)
				} else {
					log.Printf("Re-registration successful: %s", regResp.Message)
				}
			} else {
				log.Println("Heartbeat sent successfully")
			}
		}
	}()

	log.Println("Agent started successfully, waiting...")
	select {}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
