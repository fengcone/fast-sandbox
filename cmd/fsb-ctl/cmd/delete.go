package cmd

import (
	"context"
	"fmt"
	"log"

	fastpathv1 "fast-sandbox/api/proto/v1"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/klog/v2"
)

var deleteCmd = &cobra.Command{
	Use:     "delete <sandbox-id>",
	Aliases: []string{"rm"},
	Short:   "Delete a sandbox",
	Args:    cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sandboxID := args[0]
		namespace := viper.GetString("namespace")
		klog.V(4).InfoS("CLI delete command started", "sandboxId", sandboxID, "namespace", namespace)

		client, conn := getClient()
		if conn != nil {
			defer conn.Close()
		}

		klog.V(4).InfoS("Sending DeleteSandbox request", "sandboxId", sandboxID, "namespace", namespace)
		_, err := client.DeleteSandbox(context.Background(), &fastpathv1.DeleteRequest{
			SandboxId: sandboxID,
			Namespace: namespace,
		})
		if err != nil {
			klog.ErrorS(err, "DeleteSandbox request failed", "sandboxId", sandboxID, "namespace", namespace)
			log.Fatalf("Error: %v", err)
		}

		klog.V(4).InfoS("DeleteSandbox request succeeded", "sandboxId", sandboxID)
		fmt.Printf("Sandbox %s deletion triggered\n", sandboxID)
	},
}

func init() {
	rootCmd.AddCommand(deleteCmd)
}
