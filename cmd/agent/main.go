package main

import (
	"log"
	"os"

	"fast-sandbox/internal/agent/server"
)

func main() {
	log.Println("starting sandbox agent")

	// 读取环境变量
	podName := getEnv("POD_NAME", "test-agent-pod")
	podIP := getEnv("POD_IP", "127.0.0.1")
	nodeName := getEnv("NODE_NAME", "local-node")
	namespace := getEnv("NAMESPACE", "default")
	agentPort := getEnv("AGENT_PORT", ":8081")

	log.Printf("Agent Info: PodName=%s, PodIP=%s, NodeName=%s, Namespace=%s\n", podName, podIP, nodeName, namespace)

	// 启动 HTTP Server 等待 Controller 主动拉取
	agentServer := server.NewAgentServer(agentPort)
	log.Printf("Starting Agent HTTP Server on %s\n", agentPort)

	if err := agentServer.Start(); err != nil {
		log.Fatalf("Agent server failed: %v", err)
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
