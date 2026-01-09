# Fast Sandbox E2E 测试指南

本目录采用**工业级 Case 隔离架构**，确保每个测试场景的环境独立、配置纯净且自愈。

## 🧪 测试架构设计

1.  **Case 目录化**: 每个独立的测试场景（如 `test-autoscaling`）拥有专属文件夹。
2.  **依赖自包含**: 所有的 YAML 声明都存放在 Case 目录下的 `manifests/` 中。
3.  **统一基础库**: 通过 `common.sh` 提供构建镜像、安装架构、同步 Schema、自动清理等核心能力。
4.  **最终一致性清理**: 使用 `trap cleanup_all` 确保脚本无论成功还是中途报错，都会彻底清理 CRD 和 Pod。

## 📂 核心测试场景

| 场景目录 | 验证重点 |
| :--- | :--- |
| `autoscaling-and-scheduling` | 验证基于负载的自动扩缩容与原子插槽分配。 |
| `port-mutual-exclusion` | 验证同一端口在不同沙箱间的原子互斥调度。 |
| `controlled-recovery` | 验证 Manual 重置、Agent 故障自愈及 Controller 崩溃恢复。 |
| `sandbox-auto-expiry` | 验证基于 `ExpireTime` 的声明式自动垃圾回收（GC）。 |
| `infrastructure-injection` | 验证基础设施二进制（fs-helper）的静默注入与执行。 |
| `node-janitor-recovery` | 验证 Node Janitor 是否能回收 Agent 被强制删除后的残留容器。 |

## 🛠 如何运行

每个测试场景都是一个独立的可执行脚本。

```bash
# 运行指定测试
./test/e2e/controlled-recovery/test.sh
```

**环境变量支持:**
- `CLUSTER_NAME`: 指定 Kind 集群名称 (默认: `fast-sandbox`)。

## 🔍 故障排查

1.  **Schema 同步等待**: 脚本会自动等待 API Server 的 OpenAPI 缓存刷新，若提示 `Timeout waiting for CRD schema sync`，请检查 API Server 负载。
2.  **查看实时日志**: 测试运行期间，可以使用以下命令观察控制器：
    ```bash
    kubectl logs -l control-plane=controller-manager -f
    ```
3.  **底层调试**: 进入 Kind 节点查看容器：
    ```bash
    docker exec -it fast-sandbox-control-plane ctr -n k8s.io containers ls
    ```

## ⚠️ 开发原则
*   **严禁跨 Case 共享 YAML**: 避免副作用干扰。
*   **配置唯一真理**: 始终从根目录 `config/crd/` 引用 CRD，确保测试的是最新代码。
