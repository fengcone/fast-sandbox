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

	podName := getEnv("POD_NAME", "")
	podIP := getEnv("POD_IP", "")
	nodeName := getEnv("NODE_NAME", "")
	namespace := getEnv("NAMESPACE", "")
	agentPort := getEnv("AGENT_PORT", ":5758")
	runtimeTypeStr := getEnv("RUNTIME_TYPE", "container")
	runtimeSocket := getEnv("RUNTIME_SOCKET", "")

	log.Printf("Agent Info: PodName=%s, PodIP=%s, NodeName=%s, Namespace=%s\n", podName, podIP, nodeName, namespace)
	log.Printf("Runtime: Type=%s, Socket=%s\n", runtimeTypeStr, runtimeSocket)

	ctx := context.Background()
	var rt runtime.Runtime
	var err error

	rt, err = runtime.NewRuntime(ctx, runtime.RuntimeType(runtimeTypeStr), runtimeSocket)

	if err != nil {
		log.Fatalf("Failed to initialize runtime: %v", err)
	}
	defer rt.Close()

	rt.SetNamespace(namespace)

	log.Printf("Runtime initialized successfully: %s\n", runtimeTypeStr)

	sandboxManager := runtime.NewSandboxManager(rt)
	defer sandboxManager.Close()

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
