package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"time"

	fastpathv1 "fast-sandbox/api/proto/v1"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var follow bool

var logsCmd = &cobra.Command{
	Use:   "logs <sandbox-name> [-f]",
	Short: "Stream sandbox logs",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		
		// 1. 获取 Sandbox 信息以找到 Agent
		client, conn := getClient()
		if conn != nil {
			defer conn.Close()
		}

		info, err := client.GetSandbox(context.Background(), &fastpathv1.GetRequest{
			SandboxId: name,
			Namespace: viper.GetString("namespace"),
		})
		if err != nil {
			log.Fatalf("Failed to get sandbox info: %v", err)
		}

		if info.AgentPod == "" {
			log.Fatal("Sandbox is not assigned to any agent yet.")
		}

		// 2. 建立连接通道
		// 尝试直连 Agent IP (集群内或扁平网络)
		// 但通常外网无法访问 Pod IP。我们采用 port-forward 策略作为兜底。
		
		localPort, pfCmd, err := startPortForward(info.AgentPod, viper.GetString("namespace"))
		if err != nil {
			log.Fatalf("Failed to start port-forward: %v", err)
		}
		defer func() {
			if pfCmd != nil && pfCmd.Process != nil {
				pfCmd.Process.Kill()
			}
		}()

		// 3. 发起 HTTP 请求
		url := fmt.Sprintf("http://localhost:%d/api/v1/agent/logs?sandboxId=%s&follow=%t", localPort, name, follow)
		resp, err := http.Get(url)
		if err != nil {
			log.Fatalf("Failed to connect to agent: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			log.Fatalf("Agent returned error: %s", string(body))
		}

		// 4. 流式拷贝日志到 Stdout
		// 捕获中断信号以优雅退出
		go func() {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt)
			<-
sigCh
			resp.Body.Close()
			os.Exit(0)
		}()

		if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
			// io.Copy 可能会因为连接断开报错，正常退出时不应报错
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				log.Printf("Log stream ended: %v", err)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
	logsCmd.Flags().BoolVarP(&follow, "follow", "f", false, "Specify if the logs should be streamed")
}

// startPortForward 启动 kubectl port-forward
func startPortForward(podName, namespace string) (int, *exec.Cmd, error) {
	// 获取一个随机空闲端口
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	fmt.Printf("Forwarding local port %d to pod %s...\n", port, podName)

	cmd := exec.Command("kubectl", "port-forward", fmt.Sprintf("pod/%s", podName), fmt.Sprintf("%d:8081", port), "-n", namespace)
	cmd.Stdout = os.Stdout // Debug usage
	cmd.Stderr = os.Stderr
	
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}

	// 等待端口就绪
	for i := 0; i < 20; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return port, cmd, nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	cmd.Process.Kill()
	return 0, nil, fmt.Errorf("timed out waiting for port-forward")
}
