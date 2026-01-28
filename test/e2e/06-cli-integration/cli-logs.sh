#!/bin/bash

describe() {
    echo "CLI Logs - 验证 Agent 日志流式传输与 CLI 集成"
}

run() {
    # 强制清理可能的残留进程
    pkill -f "kubectl port-forward" || true

    CLIENT_BIN="$ROOT_DIR/bin/fsb-ctl"
    if [ ! -f "$CLIENT_BIN" ]; then
        echo "  编译 fsb-ctl..."
        cd "$ROOT_DIR" && go build -o bin/fsb-ctl ./cmd/fsb-ctl && cd - >/dev/null
    fi

    # 获取 Controller 命名空间
    CTRL_NS=$(kubectl get deployment fast-sandbox-controller -A -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null || echo "default")

    # 创建资源池
    POOL="logs-test-pool-$RANDOM"
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: $POOL }
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

    # 建立 Controller port-forward (fsb-ctl run 需要 Fast-Path gRPC 连接)
    kubectl port-forward deployment/fast-sandbox-controller -n "$CTRL_NS" 9090:9090 >/dev/null 2>&1 &
    CTRL_PF_PID=$!
    wait_for_condition "nc -z localhost 9090" 15 "Controller port-forward ready"

    # 1. 启动产生日志的 Sandbox
    SB_NAME="sb-logs-$RANDOM"
    echo "  启动 Sandbox ($SB_NAME)..."

    # 使用配置文件方式创建 Sandbox，因为需要执行复杂的 shell 命令
    # 注意：使用 strong 模式避免 Fast-Path gRPC server 的时序依赖
    CONFIG_FILE="/tmp/fsb-logs-test-$RANDOM.yaml"
    cat > "$CONFIG_FILE" <<EOF
image: docker.io/library/alpine:latest
pool_ref: $POOL
consistency_mode: strong
command: ["/bin/sh"]
args: ["-c", "echo Log-Line-1 && sleep 1 && echo Log-Line-2 && sleep 3600"]
EOF

    OUT=$("$CLIENT_BIN" run "$SB_NAME" \
        --pool="$POOL" \
        --namespace="$TEST_NS" \
        -f "$CONFIG_FILE" 2>&1)

    rm -f "$CONFIG_FILE"

    if ! echo "$OUT" | grep -q "successfully"; then
        echo "  ❌ 沙箱创建失败: $OUT"
        return 1
    fi

    # 2. 获取 Agent Pod 并建立转发 (使用动态端口)
    # 等待 Sandbox 分配完成并稳定
    if ! wait_for_condition "kubectl get sandbox '$SB_NAME' -n '$TEST_NS' -o jsonpath='{.status.assignedPod}' | grep -q '.'" 30 "Sandbox assigned"; then
        echo "  ❌ Sandbox 分配超时"
        return 1
    fi

    # 等待 sandbox 完全就绪（phase=Bound/Running 且容器已启动）
    echo "  等待 Sandbox 完全就绪..."
    local ready=false
    for i in {1..15}; do
        PHASE=$(kubectl get sandbox "$SB_NAME" -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [ "$PHASE" = "Bound" ] || [ "$PHASE" = "Running" ]; then
            ready=true
            break
        fi
        sleep 1
    done
    if [ "$ready" = "false" ]; then
        echo "  ❌ Sandbox 就绪超时"
        return 1
    fi
    # 额外等待容器输出日志
    sleep 3

    AGENT_POD=$(kubectl get sandbox "$SB_NAME" -n "$TEST_NS" -o jsonpath='{.status.assignedPod}')
    if [ -z "$AGENT_POD" ]; then
        echo "  ❌ 未找到 Agent Pod"
        return 1
    fi

    # 获取随机空闲端口
    PF_PORT=0
    for try in {1..10}; do
        CANDIDATE_PORT=$((10000 + RANDOM % 50000))
        if nc -z localhost "$CANDIDATE_PORT" 2>/dev/null; then
            continue
        fi
        PF_PORT=$CANDIDATE_PORT
        break
    done

    if [ "$PF_PORT" -eq 0 ]; then
        echo "  ❌ 无法获取空闲端口"
        return 1
    fi

    echo "  Agent Pod: $AGENT_POD"
    kubectl port-forward "pod/$AGENT_POD" -n "$TEST_NS" "$PF_PORT:5758" >/dev/null 2>&1 &
    PF_PID=$!

    # 等待端口就绪
    local port_ready=false
    for i in {1..10}; do
        if nc -z localhost "$PF_PORT" 2>/dev/null; then
            port_ready=true
            break
        fi
        sleep 1
    done

    if [ "$port_ready" = "false" ]; then
        echo "  ❌ Port-forward 建立失败"
        cleanup_port_forward "$PF_PID"
        return 1
    fi

    # 3. 使用 curl 验证 Agent API
    echo "  验证 Agent HTTP API..."
    CURL_OUT=$(curl -s --max-time 5 "http://localhost:$PF_PORT/api/v1/agent/logs?sandboxId=$SB_NAME")
    if echo "$CURL_OUT" | grep -q "Log-Line-1"; then
        echo "  ✓ Curl 获取日志成功"
    else
        echo "  ❌ Curl 获取日志失败: $CURL_OUT"
        kubectl logs "$AGENT_POD" -n "$TEST_NS" -c agent --tail=20
        cleanup_port_forward "$PF_PID"
        return 1
    fi

    # 4. 使用 CLI 验证 (CLI 会自己建立 port-forward)
    cleanup_port_forward "$PF_PID"

    echo "  验证 CLI logs 命令..."
    CLI_LOGS=$("$CLIENT_BIN" logs "$SB_NAME" -n "$TEST_NS" 2>&1)
    if echo "$CLI_LOGS" | grep -q "Log-Line-1"; then
        echo "  ✓ CLI logs 获取成功"
    else
        echo "  ❌ CLI logs 失败: $CLI_LOGS"
        return 1
    fi

    # 清理
    cleanup_port_forward "$CTRL_PF_PID"
    kubectl delete sandboxpool "$POOL" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    return 0
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
        # 如果仍在运行，强制终止
        kill -9 "$pid" 2>/dev/null || true
    fi
}
