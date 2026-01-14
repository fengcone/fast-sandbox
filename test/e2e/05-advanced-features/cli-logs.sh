#!/bin/bash

describe() {
    echo "CLI Logs - 验证 fsb-ctl logs 命令和自动 Port-Forward 机制"
}

run() {
    # 确保 CLI 二进制存在
    CLIENT_BIN="$ROOT_DIR/bin/fsb-ctl"
    if [ ! -f "$CLIENT_BIN" ]; then
        echo "  编译 fsb-ctl..."
        cd "$ROOT_DIR" && go build -o bin/fsb-ctl ./cmd/fsb-ctl && cd - >/dev/null
    fi

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

    # 1. 使用 CLI 启动一个会打印日志的 Sandbox
    SB_NAME="sb-logs-$RANDOM"
    echo "  使用 fsb-ctl 启动沙箱 ($SB_NAME)..."
    
    # 打印 10 行日志，每秒一行
    CMD='for i in $(seq 1 10); do echo "Log line $i"; sleep 1; done'
    
    OUT=$("$CLIENT_BIN" run "$SB_NAME" \
        --image="docker.io/library/alpine:latest" \
        --pool="$POOL" \
        --namespace="$TEST_NS" \
        "/bin/sh" "-c" "$CMD" 2>&1)
    
    if ! echo "$OUT" | grep -q "successfully"; then
        echo "  ❌ 沙箱创建失败: $OUT"
        return 1
    fi
    echo "  ✓ 沙箱创建成功"

    # 2. 验证 Get/List
    echo "  验证 Get/List..."
    if ! "$CLIENT_BIN" list -n "$TEST_NS" | grep -q "$SB_NAME"; then
        echo "  ❌ List 命令未找到沙箱"
        return 1
    fi
    if ! "$CLIENT_BIN" get "$SB_NAME" -n "$TEST_NS" | grep -q "$SB_NAME"; then
        echo "  ❌ Get 命令未找到沙箱"
        return 1
    fi

    # 3. 验证 Logs (非 Follow)
    echo "  验证 Logs (Snapshot)..."
    sleep 5 # 等待产生一些日志
    SNAPSHOT_LOG=$( "$CLIENT_BIN" logs "$SB_NAME" -n "$TEST_NS" 2>&1 )
    if echo "$SNAPSHOT_LOG" | grep -q "Log line"; then
        echo "  ✓ 静态日志获取成功"
    else
        echo "  ❌ 静态日志获取失败: $SNAPSHOT_LOG"
        # 调试
        kubectl logs -l app=sandbox-agent -n "$TEST_NS" --tail=20
        return 1
    fi

    # 4. 验证 Logs (Follow 模式)
    echo "  验证 Logs (Follow)..."
    LOG_FILE="/tmp/fsb-logs-$SB_NAME.txt"
    echo "  启动 logs -f ..."
    "$CLIENT_BIN" logs "$SB_NAME" -n "$TEST_NS" -f > "$LOG_FILE" 2>&1 &
    LOG_PID=$!
    
    echo "  等待日志流传输 (10s)..."
    sleep 10
    
    # 检查进程是否还在
    if ps -p $LOG_PID > /dev/null; then
        echo "  Logs 进程仍在运行，发送 SIGTERM..."
        kill $LOG_PID
    else
        echo "  Logs 进程已提前退出"
    fi

    # 验证日志内容
    echo "  检查日志内容..."
    if grep -q "Log line" "$LOG_FILE"; then
        echo "  ✓ 成功读取到流式日志 (至少包含 line)"
        cat "$LOG_FILE"
        rm "$LOG_FILE"
    else
        echo "  ❌ 日志内容不匹配或为空"
        echo "--- LOG CONTENT START ---"
        cat "$LOG_FILE"
        echo "--- LOG CONTENT END ---"
        rm "$LOG_FILE"
        return 1
    fi

    # 清理
    kubectl delete sandboxpool "$POOL" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    
    return 0
}
