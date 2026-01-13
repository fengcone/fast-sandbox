# fsb-ctl 产品设计方案

## 1. 产品定位与命名
- **名称**: `fsb-ctl` (Fast Sandbox Control)
- **定位**: Fast Sandbox 的**核心交互入口**，独立于 `kubectl` 的高效生产力工具。
- **核心理念**: **极致的开发者体验 (DX)**。通过配置分层和交互式引导，简化复杂参数输入。

## 2. 配置管理 (Configuration)

引入分层配置机制，解决参数爆炸问题。

### 2.1 配置文件结构
配置文件采用 JSON 格式：

```json
{
  "endpoint": "127.0.0.1:9090",
  "namespace": "default",
  "editor": "vim"  // 可选，默认优先读取 $EDITOR
}
```

### 2.2 加载优先级 (由高到低)
1.  **命令行 Flag**: `--endpoint`, `--namespace`
2.  **当前目录配置**: `./.fsb/config` (项目级)
3.  **全局用户配置**: `~/.fsb/config` (用户级)
4.  **默认值**: `localhost:9090`, `default`

## 3. 核心交互流程：Run (交互式创建)

采用“**命令行指定名称 + 交互式编辑配置**”的模式。

### 3.1 命令格式
```bash
fsb-ctl run <sandbox-name>
```

### 3.2 执行逻辑
1.  **前置校验**: 检查 Sandbox 是否已存在。
    *   若存在 -> ❌ 报错退出。
2.  **生成模板**: 在内存中生成 YAML 配置模板。
    ```yaml
    # fsb-ctl sandbox configuration
    # Name is set via CLI argument
    
    image: ubuntu:latest
    pool_ref: default-pool
    consistency_mode: fast  # options: fast, strong
    
    # Optional: Override entrypoint and arguments
    command: ["/bin/sleep", "3600"]
    args: []
    
    # Optional: Expose ports
    exposed_ports:
      - 8080
    
    # Optional: Environment variables
    envs:
      KEY: value
    ```
3.  **交互编辑**: 调用编辑器打开临时文件。
4.  **提交执行**: 用户保存后，CLI 解析并发送 `CreateRequest`。
5.  **即时反馈**: 打印创建结果、分配的 Agent 和 Endpoints。

## 4. 功能扩展 (Feature Expansion)

### 4.1 任务管控
- **`list` (ls)**: 表格化展示沙箱状态，支持 `--watch`。
- **`delete` (rm)**: 删除沙箱，支持 `--force`。
- **`get <name>`**: 查看沙箱元数据（JSON/YAML），包含创建时间、节点信息等。

### 4.2 高级调试
- **`logs <name> [-f]`**:
    *   通过 gRPC 双向流实时拉取沙箱日志。
- **`exec <name> -- <command>`**:
    *   建立交互式 TTY 隧道，支持窗口调整。

### 4.3 资源池管理
- **`pool list`**: 查看资源池负载水位。

## 5. 实施计划

1.  **Phase 1: 基础设施重构**
    *   重命名项目为 `fsb-ctl`。
    *   引入 `viper` 实现配置加载。
    *   重构 `main.go`，拆分命令文件。

2.  **Phase 2: 交互式 Run**
    *   实现 YAML 模板生成与解析。
    *   集成 Editor 调用。

3.  **Phase 3: 运维三件套**
    *   实现 `get`, `logs`, `exec`。
    *   需同步增强 Agent/Controller 的 gRPC 接口。

4.  **Phase 4: 润色**
    *   增加自动补全、颜色高亮、版本管理。