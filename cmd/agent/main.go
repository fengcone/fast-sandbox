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
	// 读取运行时类型：container 或 firecracker
	runtimeTypeStr := getEnv("RUNTIME_TYPE", "container")
	runtimeSocket := getEnv("RUNTIME_SOCKET", "") // 空字符串使用默认路径

	log.Printf("Agent Info: PodName=%s, PodIP=%s, NodeName=%s, Namespace=%s\n", podName, podIP, nodeName, namespace)
	log.Printf("Runtime: Type=%s, Socket=%s\n", runtimeTypeStr, runtimeSocket)

	// 初始化容器运行时
	ctx := context.Background()
	var rt runtime.Runtime
	var err error

	rt, err = runtime.NewRuntime(ctx, runtime.RuntimeType(runtimeTypeStr), runtimeSocket)

	if err != nil {
		log.Fatalf("Failed to initialize runtime: %v", err)
	}
	defer rt.Close()

	// 设置命名空间，用于在容器标签中标记
	rt.SetNamespace(namespace)

	log.Printf("Runtime initialized successfully: %s\n", runtimeTypeStr)

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
