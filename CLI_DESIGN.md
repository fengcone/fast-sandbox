# kubectl-fastsb 设计文档

## 1. 命名规范
- **全称**: `kubectl-fastsb`
- **理由**: 避免 `fs` 与文件系统混淆，强调 `fast` (性能) 与 `sb` (sandbox) 品牌。

## 2. 核心架构
CLI 采用 **gRPC Client** 模式，直连 Controller 的 **Fast-Path 接口**。

### 2.1 交互链路
1. **指令层**: 用户执行命令 -> 构造 gRPC 请求。
2. **连接层**: 自动通过 `port-forward` (Dev) 或 Service IP (Prod) 建立长连接。
3. **展示层**: 毫秒级反馈调度结果，支持异步 CRD 状态对账展示。

## 3. 功能清单

### 3.1 任务管控
| 命令 | 说明 | 对应后台逻辑 |
| :--- | :--- | :--- |
| `run` | 极速启动沙箱 | Fast-Path `CreateSandbox` |
| `list` | 查看沙箱列表 | 内存 Registry + CRD 对账 |
| `delete` | 删除沙箱 | Fast-Path `DeleteSandbox` |
| `describe` | 查看详情 | 获取 Registry 实时元数据 |

### 3.2 高级调试
| 命令 | 说明 | 实现挑战 |
| :--- | :--- | :--- |
| `logs` | 实时流日志 | 需在 Agent 实现 gRPC Log Stream |
| `exec` | 交互式终端 | 需实现 WebSocket/gRPC 代理隧道 |

## 4. 实施路线
1. **V1 (基础版)**: 实现 `run`, `list`, `delete`。
2. **V2 (调试版)**: 实现 `logs`, `describe`。
3. **V3 (增强版)**: 实现 `exec`, `pool` 管理。
