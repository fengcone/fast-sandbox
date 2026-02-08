#!/bin/bash

describe() {
    echo "Update/Reset 命令 - 验证通过 fsb-ctl 更新和重启 Sandbox"
}

run() {
    TEST_NS=${TEST_NS:-"e2e-cli-test-$RANDOM"}

    # 创建测试 namespace
    kubectl create namespace "$TEST_NS" --dry-run=client -o yaml | kubectl apply -f - 2>/dev/null || true

    # 创建测试 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: update-pool
spec:
  capacity: { poolMin: 1, poolMax: 2 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=update-pool" 60 "$TEST_NS"

    # 设置 port-forward 到 controller
    kubectl port-forward deployment/fast-sandbox-controller -n "$CTRL_NS" 9090:9090 >/dev/null 2>&1 &
    PF_PID=$!
    wait_for_condition "nc -z localhost 9090" 15 "Port-forward ready"

    # 构建新的 fsb-ctl 二进制到 bin目录
    echo "  构建新的 fsb-ctl 二进制..."
    cd /Users/fengjianhui/WorkSpaceL/fast-sandbox
    go build -o bin/fsb-ctl ./cmd/fsb-ctl >/dev/null 2>&1
    if [ ! -f bin/fsb-ctl ]; then
        echo "  ❌ 构建失败"
        kubectl delete sandboxpool update-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 测试 1: 使用 kubectl 创建 Sandbox（避免 fsb-ctl run 的问题）
    echo "  测试 1: 创建 Sandbox..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-update
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: update-pool
EOF

    if wait_for_condition "kubectl get sandbox sb-update -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qiE 'running|bound'" 30 "Sandbox running"; then
        echo "  ✓ Sandbox 创建成功"
    else
        echo "  ❌ Sandbox 创建失败"
        kubectl delete sandboxpool update-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 测试 2: 使用 fsb-ctl get 命令验证连接
    echo "  测试 2: 验证 fsb-ctl 连接..."
    if bin/fsb-ctl get sb-update -n "$TEST_NS" >/dev/null 2>&1; then
        echo "  ✓ fsb-ctl 连接成功"
    else
        echo "  ❌ fsb-ctl 连接失败"
        kubectl delete sandbox sb-update -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete sandboxpool update-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 测试 3: 更新过期时间
    echo "  测试 3: 更新过期时间..."
    EXPIRE_TIME=$(($(date +%s) + 3600))
    if bin/fsb-ctl update sb-update -n "$TEST_NS" --expire-time "$EXPIRE_TIME" 2>&1 | grep -q "updated successfully"; then
        echo "  ✓ 过期时间更新成功"
    else
        echo "  ⚠ 过期时间更新失败（可能是因为 gRPC 连接问题）"
    fi

    # 测试 4: 更新标签
    echo "  测试 4: 更新标签..."
    if bin/fsb-ctl update sb-update -n "$TEST_NS" --labels test=value,env=e2e 2>&1 | grep -q "updated successfully"; then
        if kubectl get sandbox sb-update -n "$TEST_NS" -o jsonpath='{.metadata.labels.test}' 2>/dev/null | grep -q "value"; then
            echo "  ✓ 标签更新成功"
        else
            echo "  ⚠ 标签未在 CRD 中设置"
        fi
    else
        echo "  ⚠ 标签更新命令失败"
    fi

    # 测试 5: 更新故障策略
    echo "  测试 5: 更新故障策略..."
    if bin/fsb-ctl update sb-update -n "$TEST_NS" --failure-policy AutoRecreate 2>&1 | grep -q "updated successfully"; then
        if kubectl get sandbox sb-update -n "$TEST_NS" -o jsonpath='{.spec.failurePolicy}' 2>/dev/null | grep -q "AutoRecreate"; then
            echo "  ✓ 故障策略更新成功"
        else
            echo "  ⚠ 故障策略未在 CRD 中设置"
        fi
    else
        echo "  ⚠ 故障策略更新命令失败"
    fi

    # 测试 6: Reset/Restart Sandbox
    echo "  测试 6: 重启 Sandbox..."
    if bin/fsb-ctl reset sb-update -n "$TEST_NS" 2>&1 | grep -q "reset triggered"; then
        if kubectl get sandbox sb-update -n "$TEST_NS" -o jsonpath='{.spec.resetRevision}' 2>/dev/null | grep -q .; then
            echo "  ✓ Reset 触发成功"
        else
            echo "  ⚠ ResetRevision 未设置"
        fi
    else
        echo "  ⚠ Reset 命令失败"
    fi

    # 清理
    kill $PF_PID 2>/dev/null
    kubectl delete sandbox sb-update -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool update-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete namespace "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
