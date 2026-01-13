package cmd

import (
	"fmt"
	"log"
	"os"

	fastpathv1 "fast-sandbox/api/proto/v1"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	cfgFile   string
	endpoint  string
	namespace string
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "fsb-ctl",
	Short: "Fast Sandbox Control - High performance container management",
	Long: `fsb-ctl is the official CLI for Fast Sandbox.
It provides a developer-friendly interface to manage sandboxes with millisecond latency.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// 全局 Flag
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./.fsb/config.json)")
	rootCmd.PersistentFlags().StringVar(&endpoint, "endpoint", "localhost:9090", "Controller gRPC endpoint")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "default", "Kubernetes namespace")

	// 绑定 Flag 到 Viper
	viper.BindPFlag("endpoint", rootCmd.PersistentFlags().Lookup("endpoint"))
	viper.BindPFlag("namespace", rootCmd.PersistentFlags().Lookup("namespace"))
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		// 1. 查找当前目录 .fsb
		viper.AddConfigPath("./.fsb")
		// 2. 查找用户目录 .fsb
		home, err := os.UserHomeDir()
		if err == nil {
			viper.AddConfigPath(home + "/.fsb")
		}
		viper.SetConfigName("config")
		viper.SetConfigType("json")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// 如果找到配置文件，则读取
	if err := viper.ReadInConfig(); err == nil {
		//fmt.Println("Using config file:", viper.ConfigFileUsed())
	}
}

// ClientFactory 允许在测试中替换 gRPC 客户端
var ClientFactory = defaultClientFactory

func defaultClientFactory() (fastpathv1.FastPathServiceClient, *grpc.ClientConn, error) {
	// 优先从 Viper 获取配置（Flag > Config > Default）
	ep := viper.GetString("endpoint")

	conn, err := grpc.Dial(ep, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to %s: %v", ep, err)
	}
	return fastpathv1.NewFastPathServiceClient(conn), conn, nil
}

// Helper: 获取 gRPC 客户端
func getClient() (fastpathv1.FastPathServiceClient, *grpc.ClientConn) {
	client, conn, err := ClientFactory()
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	return client, conn
}
