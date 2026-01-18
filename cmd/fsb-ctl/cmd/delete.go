package cmd

import (
	"context"
	"fmt"
	"log"

	fastpathv1 "fast-sandbox/api/proto/v1"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var deleteCmd = &cobra.Command{
	Use:     "delete <sandbox-id>",
	Aliases: []string{"rm"},
	Short:   "Delete a sandbox",
	Args:    cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, conn := getClient()
		if conn != nil {
			defer conn.Close()
		}

		_, err := client.DeleteSandbox(context.Background(), &fastpathv1.DeleteRequest{
			SandboxId: args[0],
			Namespace: viper.GetString("namespace"),
		})
		if err != nil {
			log.Fatalf("Error: %v", err)
		}
		fmt.Printf("Sandbox %s deletion triggered\n", args[0])
	},
}

func init() {
	rootCmd.AddCommand(deleteCmd)
}
