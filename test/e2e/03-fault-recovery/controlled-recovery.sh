#!/bin/bash

describe() {
    echo "受控恢复 - 验证手动重置和自动自愈 (AutoRecreate) 机制"
}

run() {
    # 创建测试 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: recovery-pool
spec:
  capacity: { poolMin: 1, poolMax: 2 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=recovery-pool" 60 "$TEST_NS"

    # 测试 1: 手动重置 (ResetRevision)
    echo "  测试 1: 手动重置 (ResetRevision)..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-recovery
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: recovery-pool
EOF

    sleep 15
    OLD_ID=$(kubectl get sandbox sb-recovery -n "$TEST_NS" -o jsonpath='{.status.sandboxID}' 2>/dev/null || echo "")
    OLD_POD=$(kubectl get sandbox sb-recovery -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
    echo "  原始 SandboxID: $OLD_ID on $OLD_POD"

    # 触发重置：更新 resetRevision
    NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    kubectl patch sandbox sb-recovery -n "$TEST_NS" --type='merge' -p "{\"spec\": {\"resetRevision\": \"$NOW\"}}" >/dev/null 2>&1

    echo "  等待重置执行..."
    local count=0
    for i in $(seq 1 20); do
        ACCEPTED=$(kubectl get sandbox sb-recovery -n "$TEST_NS" -o jsonpath='{.status.acceptedResetRevision}' 2>/dev/null || echo "")
        if [ "$ACCEPTED" = "$NOW" ]; then
            echo "  ✓ 手动重置成功被 Controller 确认"
            break
        fi
        if [ $i -eq 20 ]; then
            echo "  ❌ 重置未被确认"
            return 1
        fi
        sleep 3
    done

    # 测试 2: 自动自愈 (AutoRecreate)
    echo "  测试 2: 自动自愈 (AutoRecreate, Timeout=15s)..."
    kubectl patch sandbox sb-recovery -n "$TEST_NS" --type='merge' -p '{"spec": {"failurePolicy": "AutoRecreate", "recoveryTimeoutSeconds": 15}}' >/dev/null 2>&1

    echo "  删除 Agent Pod 触发断连..."
    kubectl delete pod "$OLD_POD" -n "$TEST_NS" --force --grace-period=0 >/dev/null 2>&1

    echo "  等待 AutoRecreate 触发..."
    local count=0
    for i in $(seq 1 30); do
        PHASE=$(kubectl get sandbox sb-recovery -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        ASSIGNED=$(kubectl get sandbox sb-recovery -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
        # 如果 assignedPod 变了且非空，说明触发了重调度
        if [ -n "$ASSIGNED" ] && [ "$ASSIGNED" != "$OLD_POD" ]; then
            echo "  ✓ 自动自愈触发成功！重新调度到 $ASSIGNED"

            # 清理
            kubectl delete sandbox sb-recovery -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            kubectl delete sandboxpool recovery-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            return 0
        fi
        sleep 5
    done

    echo "  ❌ 自动自愈未触发"
    return 1
}
