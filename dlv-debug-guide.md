# Agent 远程调试指南

## 概述

Agent Pod 已经配置了 dlv (Delve) 调试器，可以通过 `kubectl port-forward` 从本地 IDE 连接进行远程调试。

## 前置条件

- Agent 镜像已构建并包含 dlv 调试器
- Pod 已部署到 KIND 集群
- 本地安装了 kubectl 并配置好集群访问

## 步骤

### 1. 查找 Agent Pod

```bash
kubectl get pods -n default -l sandbox.fast.io/pool=test-sandbox-pool
```

输出示例：
```
NAME                            READY   STATUS    RESTARTS   AGE
test-sandbox-pool-agent-czfw8   1/1     Running   0          2m
test-sandbox-pool-agent-z5nnp   1/1     Running   0          2m
```

### 2. 建立端口转发

选择一个 Pod（例如第一个），执行：

```bash
POD_NAME=test-sandbox-pool-agent-czfw8
kubectl port-forward -n default $POD_NAME 2345:2345
```

这会将本地的 2345 端口转发到 Pod 的 2345 端口（dlv 监听端口）。

保持这个终端窗口运行，不要关闭。

### 3. 配置 IDE

#### GoLand / IntelliJ IDEA

1. 打开 Run -> Edit Configurations
2. 点击 `+` 添加新配置，选择 `Go Remote`
3. 配置如下：
   - **Name**: `Agent Debug`
   - **Host**: `localhost`
   - **Port**: `2345`
   - **On disconnect**: `Leave it running`
4. 点击 Apply 和 OK
5. 在代码中设置断点
6. 点击 Debug 按钮（或按 Shift+F9）

#### VSCode

1. 创建或编辑 `.vscode/launch.json`：

```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "Connect to Agent Pod",
            "type": "go",
            "request": "attach",
            "mode": "remote",
            "remotePath": "/workspace",
            "port": 2345,
            "host": "localhost"
        }
    ]
}
```

2. 在代码中设置断点
3. 按 F5 或点击 Debug 按钮

### 4. 验证连接

连接成功后：
- IDE 会显示 "Connected to debugger"
- 可以在代码中设置断点
- Agent 处理请求时会在断点处暂停

### 5. 测试调试

创建一个 SandboxClaim 触发 Agent 处理：

```bash
kubectl apply -f - <<EOF
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxClaim
metadata:
  name: debug-test-claim
  namespace: default
spec:
  image: nginx:latest
  cpu: "100m"
  memory: "128Mi"
  port: 8080
  poolRef:
    name: test-sandbox-pool
    namespace: default
EOF
```

## 端口说明

- **2345**: dlv 调试器端口
- **8081**: Agent HTTP API 端口

## 常见问题

### 1. 连接超时

**原因**: port-forward 进程可能已停止

**解决**: 检查 port-forward 进程是否运行：
```bash
ps aux | grep "port-forward"
```

如果没有运行，重新执行端口转发命令。

### 2. 无法设置断点

**原因**: 源代码路径不匹配

**解决**: 
- 确保 IDE 中打开的代码路径与编译时的路径一致
- 在 VSCode 中设置 `remotePath: "/workspace"`

### 3. 多个 Agent Pod 如何选择？

如果有多个 Agent Pod，可以：
1. 使用不同的本地端口转发到不同的 Pod：
   ```bash
   kubectl port-forward pod1 2345:2345 &
   kubectl port-forward pod2 2346:2345 &
   ```
2. 在 IDE 中创建多个调试配置，使用不同端口

## 停止调试

1. 在 IDE 中点击停止调试按钮
2. 停止 port-forward 进程：
   ```bash
   pkill -f "port-forward.*2345"
   ```

## 生产环境注意事项

⚠️ **重要**: dlv 调试器会影响性能，仅用于开发/测试环境！

生产环境应该使用标准的 ENTRYPOINT：
```dockerfile
# 生产环境
ENTRYPOINT ["./agent"]

# 调试环境
# ENTRYPOINT ["/go/bin/dlv", "--listen=:2345", "--headless=true", "--api-version=2", "--accept-multiclient", "exec", "/workspace/agent", "--"]
```

## 参考资料

- [Delve 官方文档](https://github.com/go-delve/delve)
- [GoLand 远程调试](https://www.jetbrains.com/help/go/attach-to-running-go-processes-with-debugger.html)
- [VSCode Go 调试](https://github.com/golang/vscode-go/blob/master/docs/debugging.md)
