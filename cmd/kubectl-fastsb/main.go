package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"text/tabwriter"
	"time"

	fastpathv1 "fast-sandbox/api/proto/v1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	addr      string
	namespace string
	pool      string
	mode      string
	ports     []int32
	name      string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "kubectl-fastsb",
		Short: "Fast Sandbox CLI - High performance container management",
	}

	rootCmd.PersistentFlags().StringVar(&addr, "addr", "localhost:9090", "Controller gRPC address")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "default", "Kubernetes namespace")

	// 1. RUN 命令
	runCmd := &cobra.Command{
		Use:   "run <image> [command] [args...]",
		Short: "Create a new sandbox via Fast-Path API",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			client, conn := getClient()
			defer conn.Close()

			imageName := args[0]
			var command []string
			if len(args) > 1 {
				command = args[1:]
			} else {
				// 默认值防止 runc 报错
				command = []string{"/bin/sh", "-c", "sleep 3600"}
			}

			consistency := fastpathv1.ConsistencyMode_FAST
			if mode == "strong" {
				consistency = fastpathv1.ConsistencyMode_STRONG
			}

			start := time.Now()
			resp, err := client.CreateSandbox(context.Background(), &fastpathv1.CreateRequest{
				Image:           imageName,
				PoolRef:         pool,
				ExposedPorts:    ports,
				Namespace:       namespace,
				ConsistencyMode: consistency,
				Command:         command,
				Name:            name, // 传递 name 参数
			})
			if err != nil {
				log.Fatalf("Error: %v", err)
			}

			fmt.Printf("🎉 Sandbox created successfully in %v\n", time.Since(start))
			fmt.Printf("ID:        %s\n", resp.SandboxId)
			fmt.Printf("Agent:     %s\n", resp.AgentPod)
			fmt.Printf("Endpoints: %v\n", resp.Endpoints)
		},
	}
	runCmd.Flags().StringVar(&pool, "pool", "default-pool", "Target SandboxPool")
	runCmd.Flags().StringVar(&mode, "mode", "fast", "Consistency mode (fast/strong)")
	runCmd.Flags().StringVar(&name, "name", "", "Specific sandbox name")
	runCmd.Flags().Int32SliceVar(&ports, "ports", []int32{}, "Exposed ports")
	rootCmd.AddCommand(runCmd)

	// 2. LIST 命令
	listCmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all sandboxes",
		Run: func(cmd *cobra.Command, args []string) {
			client, conn := getClient()
			defer conn.Close()

			resp, err := client.ListSandboxes(context.Background(), &fastpathv1.ListRequest{
				Namespace: namespace,
			})
			if err != nil {
				log.Fatalf("Error: %v", err)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tPHASE\tIMAGE\tAGENT\tAGE")
			for _, item := range resp.Items {
				age := time.Since(time.Unix(item.CreatedAt, 0)).Truncate(time.Second)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", item.SandboxId, item.Phase, item.Image, item.AgentPod, age)
			}
			w.Flush()
		},
	}
	rootCmd.AddCommand(listCmd)

	// 3. DELETE 命令
	deleteCmd := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete a sandbox",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			client, conn := getClient()
			defer conn.Close()

			_, err := client.DeleteSandbox(context.Background(), &fastpathv1.DeleteRequest{
				SandboxId: args[0],
				Namespace: namespace,
			})
			if err != nil {
				log.Fatalf("Error: %v", err)
			}
			fmt.Printf("Sandbox %s deletion triggered\n", args[0])
		},
	}
	rootCmd.AddCommand(deleteCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func getClient() (fastpathv1.FastPathServiceClient, *grpc.ClientConn) {
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to connect: %v", err)
	}
	return fastpathv1.NewFastPathServiceClient(conn), conn
}
