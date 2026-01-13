package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	fastpathv1 "fast-sandbox/api/proto/v1"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var outputFormat string

var getCmd = &cobra.Command{
	Use:   "get <sandbox-name>",
	Short: "Get detailed sandbox information",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, conn := getClient()
		if conn != nil {
			defer conn.Close()
		}

		resp, err := client.GetSandbox(context.Background(), &fastpathv1.GetRequest{
			SandboxId: args[0],
			Namespace: viper.GetString("namespace"),
		})
		if err != nil {
			log.Fatalf("Error: %v", err)
		}

		if outputFormat == "json" {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(resp)
		} else {
			// Default YAML-like output
			y, _ := yaml.Marshal(resp)
			fmt.Print(string(y))
			fmt.Printf("Age: %s\n", time.Since(time.Unix(resp.CreatedAt, 0)).Round(time.Second))
		}
	},
}

func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.Flags().StringVarP(&outputFormat, "output", "o", "yaml", "Output format (yaml|json)")
}
