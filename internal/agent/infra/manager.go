package infra

import (
	"fmt"
	"os"
	"path/filepath"
)

// Plugin 定义一个要注入的插件
type Plugin struct {
	Name          string `json:"name"`
	BinName       string `json:"binName"`       // infra 目录下的文件名
	ContainerPath string `json:"containerPath"` // 沙箱内的绝对路径
	IsWrapper     bool   `json:"isWrapper"`     // 是否作为命令包装器
}

type Manager struct {
	podInfraPath  string // Pod 内可见的路径 (e.g. /opt/fast-sandbox/infra)
	hostInfraPath string // 宿主机（KIND 节点内部）对应的真实路径
	plugins       []Plugin
}

func NewManager(podPath string) *Manager {
	m := &Manager{
		podInfraPath: podPath,
		plugins: []Plugin{
			{
				Name:          "system-helper",
				BinName:       "fs-helper",
				ContainerPath: "/.fs/helper",
				IsWrapper:     true,
			},
		},
	}
	m.discoverHostPath()
	return m
}

// discoverHostPath 构造 K8s 容器运行时可见的真实物理路径
func (m *Manager) discoverHostPath() {

podUID := os.Getenv("POD_UID")
	if podUID == "" {
		fmt.Printf("Warning: POD_UID not set, infra injection might fail\n")
		return
	}

	// 核心逻辑：在 K8s/KIND 环境下，emptyDir 卷在宿主机（节点）上的真实路径是有固定格式的。
	// 我们不再尝试解析 mountinfo（因为它在嵌套环境下会返回外层宿主机路径），
	// 而是直接构造节点内部的绝对路径。
	// 格式: /var/lib/kubelet/pods/<UID>/volumes/kubernetes.io~empty-dir/<VOLUME_NAME>
	m.hostInfraPath = fmt.Sprintf("/var/lib/kubelet/pods/%s/volumes/kubernetes.io~empty-dir/infra-tools", podUID)
	fmt.Printf("Determined internal node path for infra: %s\n", m.hostInfraPath)
}

func (m *Manager) GetHostPath(binName string) string {
	if m.hostInfraPath == "" {
		return ""
	}
	return filepath.Join(m.hostInfraPath, binName)
}

func (m *Manager) GetPlugins() []Plugin {
	return m.plugins
}