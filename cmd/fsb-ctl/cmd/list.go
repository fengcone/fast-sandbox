package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"text/tabwriter"
	"time"

	fastpathv1 "fast-sandbox/api/proto/v1"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all sandboxes",
	Run: func(cmd *cobra.Command, args []string) {
		client, conn := getClient()
		if conn != nil {
			defer conn.Close()
		}

		resp, err := client.ListSandboxes(context.Background(), &fastpathv1.ListRequest{
			Namespace: viper.GetString("namespace"),
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

func init() {
	rootCmd.AddCommand(listCmd)
}
