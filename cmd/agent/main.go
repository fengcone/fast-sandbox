package main

import (
	"context"
	"log"
	"os"

	"fast-sandbox/internal/agent/runtime"
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
	runtimeType := getEnv("RUNTIME_TYPE", "containerd") // 支持 containerd, docker, crio
	runtimeSocket := getEnv("RUNTIME_SOCKET", "")       // 空字符串使用默认路径

	log.Printf("Agent Info: PodName=%s, PodIP=%s, NodeName=%s, Namespace=%s\n", podName, podIP, nodeName, namespace)
	log.Printf("Runtime: Type=%s, Socket=%s\n", runtimeType, runtimeSocket)

	// 初始化容器运行时
	ctx := context.Background()
	var rt runtime.Runtime
	var err error

	switch runtimeType {
	case "containerd":
		rt, err = runtime.NewRuntime(ctx, runtime.RuntimeTypeContainerd, runtimeSocket)
	case "docker":
		rt, err = runtime.NewRuntime(ctx, runtime.RuntimeTypeDocker, runtimeSocket)
	case "crio":
		rt, err = runtime.NewRuntime(ctx, runtime.RuntimeTypeCRIO, runtimeSocket)
	default:
		log.Fatalf("Unsupported runtime type: %s", runtimeType)
	}

	if err != nil {
		log.Fatalf("Failed to initialize runtime: %v", err)
	}
	defer rt.Close()

	log.Printf("Runtime initialized successfully: %s\n", runtimeType)

	// 创建 SandboxManager
	sandboxManager := runtime.NewSandboxManager(rt)
	defer sandboxManager.Close()

	// 启动 HTTP Server 等待 Controller 主动拉取
	agentServer := server.NewAgentServer(agentPort, sandboxManager)
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
