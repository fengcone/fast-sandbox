package main

import (
	"context"
	"fmt"
	"log"
	"time"

	fastpathv1 "fast-sandbox/api/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	conn, err := grpc.Dial("localhost:9090", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()
	client := fastpathv1.NewFastPathServiceClient(conn)

	start := time.Now()
	resp, err := client.CreateSandbox(context.Background(), &fastpathv1.CreateRequest{
		Image:   "docker.io/library/alpine:latest",
		PoolRef: "fast-path-pool",
		Command: []string{"/bin/sleep", "3600"},
	})
	if err != nil {
		log.Fatalf("CreateSandbox failed: %v", err)
	}
	duration := time.Since(start)

	fmt.Printf("🎉 SUCCESS: Sandbox created via Fast-Path!\n")
	fmt.Printf("ID: %s\n", resp.SandboxId)
	fmt.Printf("Agent: %s\n", resp.AgentPod)
	fmt.Printf("Latency: %v\n", duration)
}

