package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	fastpathv1 "fast-sandbox/api/proto/v1"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// SandboxConfig 对应 YAML 配置文件的结构
type SandboxConfig struct {
	Image           string            `yaml:"image"`
	PoolRef         string            `yaml:"pool_ref"`
	ConsistencyMode string            `yaml:"consistency_mode"` // "fast" or "strong"
	Command         []string          `yaml:"command,omitempty"`
	Args            []string          `yaml:"args,omitempty"`
	ExposedPorts    []int32           `yaml:"exposed_ports,omitempty"`
	Envs            map[string]string `yaml:"envs,omitempty"`
}

var (
	configFile string
	pool       string
	mode       string
	ports      []int32
	image      string
)

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run <sandbox-name> [command] [args...]",
	Short: "Create a new sandbox via Fast-Path API",
	Long: `Create a new sandbox using interactive mode, config file, or flags.

Modes:
  1. Interactive: fsb-ctl run my-sandbox (opens editor)
  2. File-based:  fsb-ctl run my-sandbox -f config.yaml
  3. Flag-based:  fsb-ctl run my-sandbox --image=alpine --pool=default-pool

Priority: Flags > Config File > Interactive Input
`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		
		// 1. 初始化基础配置
		config := SandboxConfig{
			PoolRef:         "default-pool",
			ConsistencyMode: "fast",
		}

		// 2. 加载配置源
		if configFile != "" {
			// A. 从文件加载
			data, err := os.ReadFile(configFile)
			if err != nil {
				log.Fatalf("Failed to read config file: %v", err)
			}
			if err := yaml.Unmarshal(data, &config); err != nil {
				log.Fatalf("Failed to parse config file: %v", err)
			}
		} else if image == "" {
			// B. 交互模式 (既没文件，也没指定镜像 flag)
			fmt.Println("Entering interactive mode...")
			if err := runInteractive(name, &config); err != nil {
				log.Fatalf("Interactive mode failed: %v", err)
			}
		}

		// 3. 应用命令行参数覆盖 (Overrule)
		// 如果 flag 被显式设置了（不为空），则覆盖配置
		if image != "" {
			config.Image = image
		}
		if pool != "" && cmd.Flags().Changed("pool") {
			config.PoolRef = pool
		}
		if mode != "" && cmd.Flags().Changed("mode") {
			config.ConsistencyMode = mode
		}
		if len(ports) > 0 {
			config.ExposedPorts = ports
		}
		
		// 处理位置参数中的 command (args[1:])
		if len(args) > 1 {
			config.Command = args[1:]
		}
		
		// 最终校验
		if config.Image == "" {
			log.Fatal("Error: image is required (via flag, file, or interactive mode)")
		}

		// 4. 执行创建
		client, conn := getClient()
		if conn != nil {
			defer conn.Close()
		}

		consistency := fastpathv1.ConsistencyMode_FAST
		if config.ConsistencyMode == "strong" {
			consistency = fastpathv1.ConsistencyMode_STRONG
		}

		start := time.Now()
		req := &fastpathv1.CreateRequest{
			Name:            name,
			Image:           config.Image,
			PoolRef:         config.PoolRef,
			ExposedPorts:    config.ExposedPorts,
			Namespace:       viper.GetString("namespace"),
			ConsistencyMode: consistency,
			Command:         config.Command,
			Args:            config.Args,
		}

		resp, err := client.CreateSandbox(context.Background(), req)
		if err != nil {
			log.Fatalf("Error: %v", err)
		}

		fmt.Printf("🎉 Sandbox created successfully in %v\n", time.Since(start))
		fmt.Printf("ID:        %s\n", resp.SandboxId)
		fmt.Printf("Agent:     %s\n", resp.AgentPod)
		fmt.Printf("Endpoints: %v\n", resp.Endpoints)
	},
}

func init() {
	rootCmd.AddCommand(runCmd)

	runCmd.Flags().StringVarP(&configFile, "file", "f", "", "Path to sandbox config file")
	runCmd.Flags().StringVar(&image, "image", "", "Container image")
	runCmd.Flags().StringVar(&pool, "pool", "default-pool", "Target SandboxPool")
	runCmd.Flags().StringVar(&mode, "mode", "fast", "Consistency mode (fast/strong)")
	runCmd.Flags().Int32SliceVar(&ports, "ports", []int32{}, "Exposed ports")
}

// 内部交互逻辑
func runInteractive(name string, config *SandboxConfig) error {
	tmpFile, err := os.CreateTemp("", "fsb-sandbox-*.yaml")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	template := defaultTemplate(name)
	if _, err := tmpFile.WriteString(template); err != nil {
		return fmt.Errorf("failed to write template: %v", err)
	}
	tmpFile.Close()

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}

	cmd := exec.Command(editor, tmpFile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor execution failed: %v", err)
	}

	content, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		return fmt.Errorf("failed to read config: %v", err)
	}

	return yaml.Unmarshal(content, config)
}

func defaultTemplate(name string) string {
	return fmt.Sprintf(`# fsb-ctl sandbox configuration
# Name: %s (set via CLI argument)

# Container image to run (Required)
image: docker.io/library/golang:1.25

# Target SandboxPool (Required)
pool_ref: default-pool

# Consistency mode: 'fast' (agent-first) or 'strong' (crd-first)
consistency_mode: fast

# Optional: Override entrypoint and arguments
command: ["/bin/sleep", "3600"]
args: []

# Optional: Expose ports
# exposed_ports:
#   - 8080

# Optional: Environment variables
# envs:
#   KEY: value
`, name)
}