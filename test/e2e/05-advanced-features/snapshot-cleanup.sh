#!/bin/bash

describe() {
    echo "Snapshot Cleanup - 验证 Sandbox 删除后快照被正确清理，允许同名 Sandbox 重建"
}

# 清理 port-forward 进程的辅助函数
cleanup_port_forward() {
    local pid=$1
    if [ -n "$pid" ]; then
        kill "$pid" 2>/dev/null || true
        # 等待进程退出，最多等待 2 秒
        for i in {1..20}; do
            if ! kill -0 "$pid" 2>/dev/null; then
                break
            fi
            sleep 0.1
        done
        # 如果还没退出，强制终止
        if kill -0 "$pid" 2>/dev/null; then
            kill -9 "$pid" 2>/dev/null || true
        fi
    fi
}

run() {
    CLIENT_BIN="$ROOT_DIR/bin/fsb-ctl"

    # 编译 fsb-ctl
    if [ ! -f "$CLIENT_BIN" ]; then
        echo "  编译 fsb-ctl..."
        cd "$ROOT_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/fsb-ctl ./cmd/fsb-ctl && cd - >/dev/null
    fi

    # 创建资源池
    POOL="snapshot-test-pool-$RANDOM"
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: $POOL
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
    wait_for_pod "fast-sandbox.io/pool=$POOL" 60 "$TEST_NS"
    # 等待 Agent HTTP 端点真正就绪
    wait_for_agent_ready "fast-sandbox.io/pool=$POOL" "$TEST_NS"

    # 建立 Controller port-forward
    CTRL_NS=$(kubectl get deployment fast-sandbox-controller -A -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null || echo "default")
    kubectl port-forward deployment/fast-sandbox-controller -n "$CTRL_NS" 9090:9090 >/dev/null 2>&1 &
    CTRL_PF_PID=$!
    wait_for_condition "nc -z localhost 9090" 15 "Controller port-forward ready"

    # === Test 1: 创建 -> 删除 -> 重建同名 Sandbox ===
    echo "  测试 1: 同名 Sandbox 重建测试"
    SB_NAME="sb-snapshot-$RANDOM"

    # 第一次创建
    OUTPUT=$("$CLIENT_BIN" run "$SB_NAME" --image docker.io/library/alpine:latest --pool "$POOL" --namespace "$TEST_NS" /bin/sleep 30 2>&1)
    if echo "$OUTPUT" | grep -q "Sandbox created successfully"; then
        echo "  ✓ 第一次创建成功"
    else
        echo "  ✗ 第一次创建失败"
        echo "$OUTPUT"
        cleanup_port_forward "$CTRL_PF_PID"
        kubectl delete sandboxpool "$POOL" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 删除 Sandbox
    DELETE_OUTPUT=$("$CLIENT_BIN" delete "$SB_NAME" --namespace "$TEST_NS" 2>&1)
    if echo "$DELETE_OUTPUT" | grep -q "deletion triggered"; then
        echo "  ✓ 删除成功"
    else
        echo "  ✗ 删除失败"
        echo "$DELETE_OUTPUT"
        cleanup_port_forward "$CTRL_PF_PID"
        kubectl delete sandbox "$SB_NAME" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete sandboxpool "$POOL" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 等待删除完成 - 增加等待时间，确保快照被清理
    echo "  等待 Sandbox 完全删除和快照清理..."
    for i in {1..20}; do
        # 等待 CRD 被完全删除
        if ! kubectl get sandbox "$SB_NAME" -n "$TEST_NS" >/dev/null 2>&1; then
            echo "  ✓ Sandbox CRD 已删除"
            break
        fi
        if [ $i -eq 20 ]; then
            echo "  ⚠ Sandbox 删除超时，但继续"
        fi
        sleep 1
    done
    # 额外等待快照清理和 agent 状态同步
    # 需要等待：agent 删除完成（最多 10s）+ 心跳间隔（2s）+ 控制器处理时间
    sleep 15

    # 第二次创建（同名）- 这是关键测试
    RECREATE_OUTPUT=$("$CLIENT_BIN" run "$SB_NAME" --image docker.io/library/alpine:latest --pool "$POOL" --namespace "$TEST_NS" /bin/sleep 30 2>&1)
    if echo "$RECREATE_OUTPUT" | grep -q "Sandbox created successfully"; then
        echo "  ✓ 同名重建成功（快照已清理）"
    else
        echo "  ✗ 同名重建失败"
        echo "$RECREATE_OUTPUT"
        cleanup_port_forward "$CTRL_PF_PID"
        kubectl delete sandbox "$SB_NAME" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete sandboxpool "$POOL" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # === Test 2: 多次创建删除循环 ===
    echo "  测试 2: 多次循环测试"
    SB_NAME2="sb-snapshot-loop-$RANDOM"
    SUCCESS=true

    for i in {1..2}; do
        OUTPUT=$("$CLIENT_BIN" run "$SB_NAME2" --image docker.io/library/alpine:latest --pool "$POOL" --namespace "$TEST_NS" /bin/sleep 10 2>&1)
        if ! echo "$OUTPUT" | grep -q "Sandbox created successfully"; then
            echo "  ✗ 循环第 $i 次创建失败"
            echo "$OUTPUT"
            SUCCESS=false
            break
        fi

        # 等待 sandbox 完全就绪（重要：让控制器状态稳定）
        sleep 3

        DELETE_OUTPUT=$("$CLIENT_BIN" delete "$SB_NAME2" --namespace "$TEST_NS" 2>&1)
        if ! echo "$DELETE_OUTPUT" | grep -q "deletion triggered"; then
            echo "  ✗ 循环第 $i 次删除失败"
            SUCCESS=false
            break
        fi

        # 等待 Sandbox 完全删除（优雅关闭最多 10 秒，加上余量）
        echo "  等待 Sandbox 完全删除和快照清理..."
        for j in {1..30}; do
            # 等待 CRD 被完全删除
            if ! kubectl get sandbox "$SB_NAME2" -n "$TEST_NS" >/dev/null 2>&1; then
                echo "  ✓ 循环第 $i 次: Sandbox CRD 已删除"
                break
            fi
            if [ $j -eq 30 ]; then
                echo "  ⚠ 循环第 $i 次: 删除超时，但继续"
            fi
            sleep 1
        done
        # 额外等待快照清理、agent 状态同步和控制器注册表更新
        # 这是关键：需要足够时间让 agent 的 asyncDelete 完成并更新控制器注册表
        # 需要等待：agent 删除完成（最多 10s）+ 心跳间隔（2s）+ 控制器处理时间
        sleep 15
    done

    if [ "$SUCCESS" = true ]; then
        echo "  ✓ 循环测试通过（2 次创建/删除）"
    else
        cleanup_port_forward "$CTRL_PF_PID"
        kubectl delete sandbox "$SB_NAME" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete sandbox "$SB_NAME2" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete sandboxpool "$POOL" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 清理
    cleanup_port_forward "$CTRL_PF_PID"
    kubectl delete sandbox "$SB_NAME" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandbox "$SB_NAME2" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool "$POOL" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
