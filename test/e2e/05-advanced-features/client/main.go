package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	fastpathv1 "fast-sandbox/api/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("addr", "localhost:9090", "Controller gRPC address")
	image := flag.String("image", "docker.io/library/alpine:latest", "Sandbox image")
	pool := flag.String("pool", "test-pool", "SandboxPool name")
	mode := flag.String("mode", "fast", "Consistency mode: fast or strong")
	port := flag.Int("port", 0, "Exposed port")
	name := flag.String("name", "", "Specific sandbox name (for orphan testing)")
	namespace := flag.String("namespace", "default", "Kubernetes namespace")
	flag.Parse()

	conn, err := grpc.Dial(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()
	client := fastpathv1.NewFastPathServiceClient(conn)

	consistency := fastpathv1.ConsistencyMode_FAST
	if *mode == "strong" {
		consistency = fastpathv1.ConsistencyMode_STRONG
	}

	ports := []int32{}
	if *port > 0 {
		ports = append(ports, int32(*port))
	}

	req := &fastpathv1.CreateRequest{
		Image:           *image,
		PoolRef:         *pool,
		ExposedPorts:    ports,
		Command:         []string{"/bin/sleep", "3600"},
		ConsistencyMode: consistency,
		Name:            *name,
		Namespace:       *namespace,
	}

	// 如果指定了名称，由于目前的 Proto 没直接支持指定名称，我们通过某种方式模拟
	// 或者我们期待后台会根据时间戳生成。
	// 在孤儿测试中，我们需要 CRD 写入失败。

	start := time.Now()
	resp, err := client.CreateSandbox(context.Background(), req)
	if err != nil {
		log.Fatalf("CreateSandbox failed: %v", err)
	}
	duration := time.Since(start)

	fmt.Printf("🎉 SUCCESS\n")
	fmt.Printf("sandbox_id %s\n", resp.SandboxId)
	fmt.Printf("agent_pod %s\n", resp.AgentPod)
	fmt.Printf("latency %v\n", duration)
}
