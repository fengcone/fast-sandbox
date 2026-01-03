package runtime

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// GetPodCgroupPath 从 /proc/self/cgroup 读取当前进程的 cgroup 路径
// 返回格式如: /kubepods/burstable/pod<uid>/<container-id>
func GetPodCgroupPath() (string, error) {
	file, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("failed to open /proc/self/cgroup: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		// cgroup v2 格式: 0::/kubepods/burstable/pod.../...
		// cgroup v1 格式: 12:cpuset:/kubepods/burstable/pod.../...
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}

		path := parts[2]
		// 查找包含 kubepods 的路径
		if strings.Contains(path, "kubepods") {
			// 提取到 pod 级别的路径，去掉容器级别
			// 例如: /kubepods/burstable/pod<uid>/<container-id> -> /kubepods/burstable/pod<uid>
			if idx := strings.LastIndex(path, "/"); idx > 0 {
				// 检查是否是容器 ID（通常是长字符串）
				lastPart := path[idx+1:]
				if len(lastPart) > 20 {
					// 这可能是容器 ID，返回 pod 级别路径
					return path[:idx], nil
				}
			}
			// 否则直接返回整个路径
			return path, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to read /proc/self/cgroup: %w", err)
	}

	return "", fmt.Errorf("kubepods cgroup not found in /proc/self/cgroup")
}

// GetPodNetNS 获取当前进程的 network namespace 路径
// 返回格式如: /proc/<pid>/ns/net
func GetPodNetNS() (string, error) {
	// 读取当前进程的 PID
	pid := os.Getpid()
	netnsPath := fmt.Sprintf("/proc/%d/ns/net", pid)

	// 检查文件是否存在
	if _, err := os.Stat(netnsPath); err != nil {
		return "", fmt.Errorf("failed to access network namespace: %w", err)
	}

	return netnsPath, nil
}

// ParseCgroupV2Path 解析 cgroup v2 路径
// 返回 (hierarchy-id, controller, path)
func ParseCgroupV2Path(line string) (string, string, string) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) != 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}
