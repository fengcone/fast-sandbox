# KIND 集群环境适配总结

## 环境信息

| 特性 | 值 |
|------|-----|
| OS | Alibaba Cloud Linux 3 (基于 RHEL/CentOS) |
| Cgroup 版本 | v1 (legacy hierarchy) |
| Systemd | 宿主机不可用，但 KIND 节点内部使用 systemd |
| Docker 版本 | 24.0.9 |
| Docker Cgroup Driver | cgroupfs |
| KIND 版本 | v0.31.0 |
| Kubernetes 版本 | v1.27.3 |

---

## 问题与解决方案

### 问题 1: Docker 权限不足

**现象**:
```
permission denied while trying to connect to the Docker daemon socket
```

**原因**: 用户不在 docker 组中。

**解决方案**:
```bash
sudo usermod -aG docker $USER
# 断开 SSH 重新登录以刷新组权限
```

---

### 问题 2: kubelet 不支持 --provider 参数

**现象**:
```
E0211 07:17:46 kubelet[1915] err="failed to parse kubelet flag: unknown flag: --provider"
```

**原因**: `--provider` 参数在 Kubernetes 1.24+ 中已被废弃。

**解决方案**: 移除 `kubeletExtraArgs` 中的 `provider: containerd` 配置。

---

### 问题 3: containerd socket 挂载导致冲突

**现象**:
```
[kubelet-check] It seems like the kubelet isn't running or healthy.
[dial tcp [::1]:10248: connect: connection refused]
```

**原因**: KIND 配置中挂载了宿主机的 containerd socket，与节点内部的 containerd 冲突。

**解决方案**: 移除 `extraMounts` 中的 containerd socket 挂载：
```yaml
# 删除以下内容
- hostPath: /run/containerd/containerd.sock
  containerPath: /run/containerd/containerd.sock
- hostPath: /run/containerd/fifo
  containerPath: /run/containerd/fifo
```

---

### 问题 4: cgroup driver 冲突

**现象**:
```
expected cgroupsPath to be of format "slice:prefix:name" for systemd cgroups,
got "/kubelet/kubepods/burstable/pod..." instead: unknown
```

**原因**:
- 宿主机使用 cgroup v1，配置了 `cgroup-driver: cgroupfs`
- 但 KIND 节点内部使用 **systemd** 作为 init 系统
- kubelet 需要使用 systemd cgroup driver，不能强制使用 cgroupfs

**解决方案**:
完全使用 KIND 默认配置，让 kubelet 自动检测并选择正确的 cgroup driver：

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
# 不添加任何 cgroup 配置
```

---

## 最终配置

```bash
# 1. 加入 docker 组
sudo usermod -aG docker $USER
# 重新 SSH 登录

# 2. 运行脚本（使用 KIND 默认配置）
./test/e2e/setup-kind.sh --skip-build
```

---

## 关键结论

1. **Cgroup v1 + systemd 节点**: KIND 默认配置即可，无需手动指定 cgroup driver
2. **KIND 节点内部是 systemd**: kubelet 会自动使用 systemd cgroup driver
3. **不要强制覆盖 cgroup driver**: 这会导致 kubelet 无法启动
4. **不要挂载外部 containerd socket**: KIND 节点有自己的 containerd

---

## 验证结果

```
集群状态:     Ready
Kubernetes:   v1.27.3
Controller:   1/1 Running
Janitor:      1/1 Ready
```
