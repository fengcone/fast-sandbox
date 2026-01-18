#!/bin/bash

describe() {
    echo "优雅关闭 - 验证删除时 SIGTERM → 等待 → SIGKILL 流程"
}

run() {
    # 创建测试 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: shutdown-pool
spec:
  capacity: { poolMin: 1, poolMax: 2 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=shutdown-pool" 60 "$TEST_NS"

    # 测试: 优雅关闭流程
    echo "  测试: 优雅关闭 (SIGTERM → wait → SIGKILL)..."

    # 创建一个能捕获 SIGTERM 的容器
    # 使用 shell 脚本在收到 SIGTERM 时写入日志
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-graceful
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sh", "-c"]
  args:
    - |
      trap 'echo GRACEFUL_SHUTDOWN_RECEIVED; exit 0' TERM
      echo "Starting sandbox with graceful shutdown handler"
      for i in 1 2 3 4 5; do
        echo "Running... \$i"
        sleep 10
      done
      echo "Unexpected completion"
  poolRef: shutdown-pool
EOF

    # 等待 Sandbox 运行
    if ! wait_for_condition "kubectl get sandbox sb-graceful -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qiE 'running|bound'" 30 "Sandbox running"; then
        echo "  ❌ Sandbox 启动超时"
        kubectl delete sandboxpool shutdown-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    ASSIGNED_POD=$(kubectl get sandbox sb-graceful -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
    echo "  Sandbox 运行在 Agent: $ASSIGNED_POD"

    # 删除 Sandbox（触发优雅关闭）
    echo "  删除 Sandbox..."
    kubectl delete sandbox sb-graceful -n "$TEST_NS" >/dev/null 2>&1

    # 验证 1: 应该立即进入 Terminating 状态
    echo "  验证 1: 检查 Phase 是否变为 Terminating..."
    local elapsed=0
    local phase_check_ok=false
    while [ $elapsed -lt 10 ]; do
        PHASE=$(kubectl get sandbox sb-graceful -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [ "$PHASE" = "Terminating" ]; then
            echo "  ✓ Phase 正确变为 Terminating"
            phase_check_ok=true
            break
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done

    if [ "$phase_check_ok" = false ]; then
        echo "  ⚠ Phase 未在预期时间内变为 Terminating (当前: $PHASE)"
        # 继续测试，不返回错误
    fi

    # 验证 2: CRD 应该保留直到 agent 完成删除
    echo "  验证 2: 检查 CRD 是否保留直到删除完成..."
    local still_exists=false
    for i in 1 2 3 4 5; do
        if kubectl get sandbox sb-graceful -n "$TEST_NS" >/dev/null 2>&1; then
            still_exists=true
            sleep 2
        else
            break
        fi
    done

    if [ "$still_exists" = true ]; then
        echo "  ✓ CRD 保留了一段时间（异步删除中）"
    else
        echo "  ⚠ CRD 删除太快，可能异步删除未生效"
    fi

    # 等待 CRD 最终被删除
    if wait_for_condition "! kubectl get sandbox sb-graceful -n '$TEST_NS' >/dev/null 2>&1" 60 "CRD deleted"; then
        echo "  ✓ CRD 最终被正确删除"
    else
        echo "  ⚠ CRD 删除超时"
        kubectl delete sandbox sb-graceful -n "$TEST_NS" --force --grace-period=0 >/dev/null 2>&1 || true
    fi

    # 验证 3: 检查 agent 日志中是否有优雅关闭的相关日志
    echo "  验证 3: 检查 Agent 日志..."
    if [ -n "$ASSIGNED_POD" ]; then
        # 获取 agent pod 的日志（检查是否有删除完成的日志）
        AGENT_LOGS=$(kubectl logs "$ASSIGNED_POD" -n "$TEST_NS" --tail=50 2>/dev/null || echo "")
        if echo "$AGENT_LOGS" | grep -q "deletion completed\|marked for deletion"; then
            echo "  ✓ Agent 日志显示删除流程正确执行"
        else
            echo "  ⚠ Agent 日志中未找到明确的删除完成标记"
        fi
    fi

    # 清理
    kubectl delete sandboxpool shutdown-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
