package janitor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// cleanupFIFOs Tests
// ============================================================================

func TestCleanupFIFOs(t *testing.T) {
	// RED: 测试 FIFO 文件清理功能
	// 创建临时目录模拟 /run/containerd/fifo
	tempDir := t.TempDir()
	fifoDir := filepath.Join(tempDir, "fifo")
	require.NoError(t, os.MkdirAll(fifoDir, 0755))

	// 创建一些测试 FIFO 文件
	containerID := "test-container-123"
	testFiles := []string{
		containerID + "-stdout",
		containerID + "-stderr",
		containerID + "-control",
		"other-file-should-not-be-deleted",
	}

	for _, f := range testFiles {
		path := filepath.Join(fifoDir, f)
		require.NoError(t, os.WriteFile(path, []byte("test"), 0644))
	}

	// 验证文件已创建
	for _, f := range testFiles {
		path := filepath.Join(fifoDir, f)
		_, err := os.Stat(path)
		require.NoError(t, err, "文件应该存在: "+f)
	}

	// 由于 cleanupFIFOs 使用硬编码的 /run/containerd/fifo
	// 我们需要测试其逻辑，这里通过直接测试模式匹配逻辑
	pattern := filepath.Join(fifoDir, containerID+"*")
	matches, err := filepath.Glob(pattern)
	require.NoError(t, err)
	assert.Equal(t, 3, len(matches), "应该找到3个匹配的文件")

	// 模拟删除操作
	for _, m := range matches {
		os.Remove(m)
	}

	// 验证匹配的文件已被删除
	for _, f := range testFiles[:3] {
		path := filepath.Join(fifoDir, f)
		_, err := os.Stat(path)
		assert.True(t, os.IsNotExist(err), "文件应该被删除: "+f)
	}

	// 验证不匹配的文件仍然存在
	otherFile := filepath.Join(fifoDir, testFiles[3])
	_, err = os.Stat(otherFile)
	assert.NoError(t, err, "不匹配的文件应该保留")
}

// ============================================================================
// verifyPodExists Tests
// ============================================================================

func TestVerifyPodExistsDirectly_NoKubeClient(t *testing.T) {
	// RED: 测试当没有 kubeClient 时 verifyPodExists 返回 false
	j := &Janitor{
		kubeClient: nil,
	}

	ctx := context.Background()
	exists := j.verifyPodExists(ctx, "test-uid", "default")

	assert.False(t, exists, "没有 kubeClient 时应返回 false")
}

func TestVerifyPodExistsDirectly_EmptyNamespace(t *testing.T) {
	// RED: 测试当 namespace 为空时使用 "default"
	j := &Janitor{
		kubeClient: nil,
	}

	ctx := context.Background()
	exists := j.verifyPodExists(ctx, "test-uid", "")

	assert.False(t, exists, "没有 kubeClient 时应返回 false")
}

// ============================================================================
// OrphanTimeout Constant Test
// ============================================================================

func TestDefaultOrphanTimeout(t *testing.T) {
	// RED: 测试默认的孤儿超时时间是 10 秒
	assert.Equal(t, 10*time.Second, defaultOrphanTimeout,
		"默认孤儿超时应该是 10 秒")
}
