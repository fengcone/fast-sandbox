package cmd

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	fastpathv1 "fast-sandbox/api/proto/v1"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	updateExpireTime     string
	updateFailurePolicy  string
	updateRecoveryTimeout int32
	updateLabels         []string
)

// updateCmd represents the update command
var updateCmd = &cobra.Command{
	Use:   "update <sandbox-id>",
	Short: "Update sandbox configuration",
	Long: `Update sandbox properties such as expire time, failure policy, or labels.

Examples:
  # Extend expiration to 1 hour from now
  fsb-ctl update my-sandbox --expire-time $(($(date +%s) + 3600))

  # Remove expiration
  fsb-ctl update my-sandbox --expire-time 0

  # Set failure policy to auto-recreate
  fsb-ctl update my-sandbox --failure-policy AutoRecreate

  # Add labels
  fsb-ctl update my-sandbox --labels env=prod,tier=backend

  # Update recovery timeout
  fsb-ctl update my-sandbox --recovery-timeout 120`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, conn := getClient()
		if conn != nil {
			defer conn.Close()
		}

		sandboxID := args[0]
		namespace := viper.GetString("namespace")

		req := &fastpathv1.UpdateRequest{
			SandboxId: sandboxID,
			Namespace: namespace,
			Labels:    make(map[string]string),
		}

		// 解析 --expire-time
		if cmd.Flags().Changed("expire-time") {
			seconds, err := parseExpireTime(updateExpireTime)
			if err != nil {
				log.Fatalf("Error: invalid expire-time: %v", err)
			}
			req.Update = &fastpathv1.UpdateRequest_ExpireTimeSeconds{
				ExpireTimeSeconds: seconds,
			}
		}

		// 解析 --failure-policy
		if cmd.Flags().Changed("failure-policy") {
			policy, err := parseFailurePolicy(updateFailurePolicy)
			if err != nil {
				log.Fatalf("Error: invalid failure-policy: %v", err)
			}
			req.Update = &fastpathv1.UpdateRequest_FailurePolicy{
				FailurePolicy: policy,
			}
		}

		// 解析 --recovery-timeout
		if cmd.Flags().Changed("recovery-timeout") {
			req.Update = &fastpathv1.UpdateRequest_RecoveryTimeoutSeconds{
				RecoveryTimeoutSeconds: updateRecoveryTimeout,
			}
		}

		// 解析 --labels
		if len(updateLabels) > 0 {
			for _, label := range updateLabels {
				parts := strings.SplitN(label, "=", 2)
				if len(parts) != 2 {
					log.Fatalf("Error: invalid label format '%s', expected key=value", label)
				}
				req.Labels[parts[0]] = parts[1]
			}
		}

		// 检查是否至少有一个更新操作
		if req.Update == nil && len(req.Labels) == 0 {
			log.Fatal("Error: at least one update field must be specified (--expire-time, --failure-policy, --recovery-timeout, or --labels)")
		}

		resp, err := client.UpdateSandbox(context.Background(), req)
		if err != nil {
			log.Fatalf("Error: %v", err)
		}

		if !resp.Success {
			log.Fatalf("Error: %s", resp.Message)
		}

		fmt.Printf("✓ Sandbox %s updated successfully\n", sandboxID)
		if resp.Sandbox != nil {
			fmt.Printf("  Phase: %s\n", resp.Sandbox.Phase)
			fmt.Printf("  Agent: %s\n", resp.Sandbox.AgentPod)
		}
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)

	updateCmd.Flags().StringVar(&updateExpireTime, "expire-time", "", "Expiration time (Unix timestamp or '0' to remove)")
	updateCmd.Flags().StringVar(&updateFailurePolicy, "failure-policy", "", "Failure policy (Manual|AutoRecreate)")
	updateCmd.Flags().Int32Var(&updateRecoveryTimeout, "recovery-timeout", 0, "Recovery timeout in seconds")
	updateCmd.Flags().StringSliceVar(&updateLabels, "labels", []string{}, "Labels to set (key=value format)")
}

// parseExpireTime 解析过期时间参数
// 支持格式：
//   - "0" - 移除过期时间
//   - 数字 - Unix 时间戳
func parseExpireTime(input string) (int64, error) {
	// 检查是否为 "0" (移除过期)
	if input == "0" {
		return 0, nil
	}

	// 尝试解析为数字 (Unix 时间戳)
	seconds, err := strconv.ParseInt(input, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid timestamp format: %w", err)
	}

	return seconds, nil
}

// parseFailurePolicy 解析故障策略
func parseFailurePolicy(input string) (fastpathv1.FailurePolicy, error) {
	switch strings.ToLower(input) {
	case "manual":
		return fastpathv1.FailurePolicy_MANUAL, nil
	case "auto-recreate", "autorecreate", "auto":
		return fastpathv1.FailurePolicy_AUTO_RECREATE, nil
	default:
		return fastpathv1.FailurePolicy_MANUAL, fmt.Errorf("unknown failure policy: %s (valid: Manual, AutoRecreate)", input)
	}
}
