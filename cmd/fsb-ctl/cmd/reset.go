package cmd

import (
	"context"
	"fmt"
	"log"
	"time"

	fastpathv1 "fast-sandbox/api/proto/v1"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// resetCmd represents the reset command
var resetCmd = &cobra.Command{
	Use:     "reset <sandbox-id>",
	Aliases: []string{"restart"},
	Short:   "Reset/Restart a sandbox",
	Long: `Trigger a sandbox reset by updating its ResetRevision field.

This will cause the controller to reschedule the sandbox to a new agent pod,
preserving the sandbox configuration.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, conn := getClient()
		if conn != nil {
			defer conn.Close()
		}

		sandboxID := args[0]
		namespace := viper.GetString("namespace")

		// 使用当前时间作为 ResetRevision
		resetRevision := time.Now().Format(time.RFC3339Nano)

		req := &fastpathv1.UpdateRequest{
			SandboxId: sandboxID,
			Namespace: namespace,
			Update: &fastpathv1.UpdateRequest_ResetRevision{
				ResetRevision: resetRevision,
			},
		}

		resp, err := client.UpdateSandbox(context.Background(), req)
		if err != nil {
			log.Fatalf("Error: %v", err)
		}

		if !resp.Success {
			log.Fatalf("Error: %s", resp.Message)
		}

		fmt.Printf("✓ Sandbox %s reset triggered\n", sandboxID)
		fmt.Printf("  The sandbox will be rescheduled to a new agent\n")
	},
}

func init() {
	rootCmd.AddCommand(resetCmd)
}
