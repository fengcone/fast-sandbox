#!/bin/bash

describe() {
    echo "CLI Cache - 验证 fsb-ctl run 交互式缓存机制"
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
    CACHE_DIR="$HOME/.fsb-ctl/cache"
    TEST_NS=${TEST_NS:-"e2e-cli-test-$RANDOM"}

    # 创建测试 namespace
    kubectl create namespace "$TEST_NS" --dry-run=client -o yaml | kubectl apply -f - 2>/dev/null || true

    # 编译 fsb-ctl
    if [ ! -f "$CLIENT_BIN" ]; then
        echo "  编译 fsb-ctl..."
        cd "$ROOT_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/fsb-ctl ./cmd/fsb-ctl && cd - >/dev/null
    fi

    # 创建资源池
    POOL="cache-test-pool-$RANDOM"
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

    # 等待 Agent 心跳同步到 Controller（确保容量信息已更新）
    echo "  等待 Agent 心跳同步..."
    sleep 3

    # 建立 Controller port-forward
    CTRL_NS=$(kubectl get deployment fast-sandbox-controller -A -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null || echo "default")
    kubectl port-forward deployment/fast-sandbox-controller -n "$CTRL_NS" 9090:9090 >/dev/null 2>&1 &
    CTRL_PF_PID=$!
    wait_for_condition "nc -z localhost 9090" 15 "Controller port-forward ready"

    # 清理旧缓存
    rm -rf "$CACHE_DIR"

    # === Test 1: 首次运行，应创建缓存 ===
    echo "  测试 1: 首次运行创建缓存"
    SB_NAME="sb-cache-first-$RANDOM"

    # 预创建缓存（模拟首次编辑后的内容）
    mkdir -p "$CACHE_DIR"
    CACHE_FILE="$CACHE_DIR/$SB_NAME.yaml"
    cat > "$CACHE_FILE" <<EOF
image: docker.io/library/alpine:latest
pool_ref: $POOL
consistency_mode: fast
command: ["/bin/sh", "-c"]
args: ["echo 'First run from cache' && sleep 60"]
EOF

    # 验证缓存文件存在
    if [ -f "$CACHE_FILE" ]; then
        echo "  ✓ 缓存文件已创建: $CACHE_FILE"
    else
        echo "  ✗ 缓存文件未创建"
        return 1
    fi

    # === Test 2: 使用缓存创建 Sandbox ===
    echo "  测试 2: 使用缓存配置创建 Sandbox"

    # 使用 -f 参数指定缓存文件来创建（需要指定 namespace）
    OUTPUT=$("$CLIENT_BIN" run "$SB_NAME" -f "$CACHE_FILE" --namespace "$TEST_NS" 2>&1)
    if echo "$OUTPUT" | grep -q "Sandbox created successfully"; then
        echo "  ✓ Sandbox 创建成功"
    else
        echo "  ✗ Sandbox 创建失败"
        echo "$OUTPUT"
        return 1
    fi

    # === Test 3: 验证缓存内容被正确读取 ===
    echo "  测试 3: 验证缓存内容正确性"

    # 检查缓存中的关键配置
    if grep -q "echo 'First run from cache'" "$CACHE_FILE"; then
        echo "  ✓ 缓存内容正确"
    else
        echo "  ✗ 缓存内容不正确"
        return 1
    fi

    # === Test 4: 清理单个缓存 ===
    echo "  测试 4: 清理单个缓存文件"
    rm -f "$CACHE_FILE"
    if [ ! -f "$CACHE_FILE" ]; then
        echo "  ✓ 缓存文件已删除"
    else
        echo "  ✗ 缓存文件删除失败"
        return 1
    fi

    # === Test 5: 缓存不存在时应使用默认模板 ===
    echo "  测试 5: 缓存不存在时的默认行为"

    # 先删除第一个 Sandbox 以释放插槽
    echo "  删除第一个 Sandbox 释放插槽..."
    "$CLIENT_BIN" delete "$SB_NAME" --namespace "$TEST_NS" >/dev/null 2>&1
    # 等待删除完成
    for i in {1..15}; do
        if ! kubectl get sandbox "$SB_NAME" -n "$TEST_NS" >/dev/null 2>&1; then
            echo "  ✓ 第一个 Sandbox 已删除"
            break
        fi
        sleep 1
    done

    # 验证默认模板的关键字段
    DEFAULT_TEMPLATE_TEST="$CACHE_DIR/default-test.yaml"
    cat > "$DEFAULT_TEMPLATE_TEST" <<EOF
image: docker.io/library/alpine:latest
pool_ref: $POOL
consistency_mode: fast
command: ["/bin/sleep", "30"]
EOF

    # 使用新配置创建
    SB_NAME2="sb-cache-default-$RANDOM"
    OUTPUT=$("$CLIENT_BIN" run "$SB_NAME2" -f "$DEFAULT_TEMPLATE_TEST" --namespace "$TEST_NS" 2>&1)
    if echo "$OUTPUT" | grep -q "Sandbox created successfully"; then
        echo "  ✓ 使用默认配置创建成功"
    else
        echo "  ✗ 默认配置创建失败"
        echo "$OUTPUT"
        return 1
    fi

    # 清理
    cleanup_port_forward "$CTRL_PF_PID"
    kubectl delete sandbox "$SB_NAME" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandbox "$SB_NAME2" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool "$POOL" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete namespace "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # 清理测试缓存目录
    rm -rf "$CACHE_DIR"

    return 0
}
