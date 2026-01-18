#!/bin/bash

describe() {
    echo "环境变量和工作目录 - 验证 Envs 和 WorkingDir 正确传递到容器"
}

# 辅助函数：通过容器日志验证环境变量（容器启动时输出到 stdout）
verify_env_via_log() {
    local agent_pod=$1
    local ns=$2
    local sandbox_id=$3
    local var_name=$4
    local expected_value=$5

    # 容器命令会输出 "ENV_VAR_NAME=value" 格式
    # 通过 Agent 日志目录读取
    local log_output=$(kubectl exec -n "$ns" "$agent_pod" -- cat /var/log/fast-sandbox/${sandbox_id}.log 2>/dev/null | grep "^${var_name}=" | cut -d= -f2)
    if [ "$log_output" = "$expected_value" ]; then
        return 0
    fi
    return 1
}

# 辅助函数：通过容器日志验证工作目录
verify_pwd_via_log() {
    local agent_pod=$1
    local ns=$2
    local sandbox_id=$3
    local expected_value=$4

    # 容器命令会输出 "PWD=/path" 格式
    local log_output=$(kubectl exec -n "$ns" "$agent_pod" -- cat /var/log/fast-sandbox/${sandbox_id}.log 2>/dev/null | grep "^PWD=" | cut -d= -f2)
    if [ "$log_output" = "$expected_value" ]; then
        return 0
    fi
    return 1
}

run() {
    # ========================================
    # Case 1: CRD 创建 - 验证 Envs
    # ========================================
    echo "  === Case 1: CRD 创建 - 验证环境变量 ==="
    POOL_1="env-test-pool-$RANDOM"
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: $POOL_1
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
    wait_for_pod "fast-sandbox.io/pool=$POOL_1" 60 "$TEST_NS"

    # 创建带环境变量的 Sandbox，容器启动时输出 env 到日志
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-env-test
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sh", "-c", "echo \"TEST_VAR=\$TEST_VAR\"; echo \"ANOTHER_VAR=\$ANOTHER_VAR\"; sleep 3600"]
  poolRef: $POOL_1
  envs:
    - name: TEST_VAR
      value: "test_value_123"
    - name: ANOTHER_VAR
      value: "another_value_456"
EOF

    echo "  等待 Sandbox 分配..."
    wait_for_condition "kubectl get sandbox sb-env-test -n '$TEST_NS' -o jsonpath='{.status.assignedPod}' 2>/dev/null | grep -q '.'" 30 "Sandbox Assigned"

    AGENT_POD=$(kubectl get sandbox sb-env-test -n "$TEST_NS" -o jsonpath='{.status.assignedPod}')
    echo "  Agent Pod: $AGENT_POD"

    # 等待容器启动并输出日志
    sleep 3

    # 验证环境变量（通过日志）
    echo "  验证环境变量..."
    if verify_env_via_log "$AGENT_POD" "$TEST_NS" "sb-env-test" "TEST_VAR" "test_value_123"; then
        echo "  ✓ 环境变量 TEST_VAR 正确: test_value_123"
    else
        # 输出实际日志用于调试
        LOG_CONTENT=$(kubectl exec -n "$TEST_NS" "$AGENT_POD" -- cat /var/log/fast-sandbox/sb-env-test.log 2>/dev/null || echo "log not found")
        echo "  ❌ 环境变量 TEST_VAR 错误. 日志内容: $LOG_CONTENT"
        kubectl delete sandboxpool $POOL_1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    if verify_env_via_log "$AGENT_POD" "$TEST_NS" "sb-env-test" "ANOTHER_VAR" "another_value_456"; then
        echo "  ✓ 环境变量 ANOTHER_VAR 正确: another_value_456"
    else
        echo "  ❌ 环境变量 ANOTHER_VAR 错误"
        kubectl delete sandboxpool $POOL_1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 清理 Case 1
    kubectl delete sandbox sb-env-test -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # ========================================
    # Case 2: CRD 创建 - 验证 WorkingDir
    # ========================================
    echo "  === Case 2: CRD 创建 - 验证工作目录 ==="

    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-workdir-test
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sh", "-c", "echo \"PWD=\$(pwd)\"; sleep 3600"]
  workingDir: /tmp
  poolRef: $POOL_1
EOF

    echo "  等待 Sandbox 分配..."
    wait_for_condition "kubectl get sandbox sb-workdir-test -n '$TEST_NS' -o jsonpath='{.status.assignedPod}' 2>/dev/null | grep -q '.'" 30 "Sandbox Assigned"

    # 等待容器启动
    sleep 3

    # 验证工作目录
    echo "  验证工作目录..."
    if verify_pwd_via_log "$AGENT_POD" "$TEST_NS" "sb-workdir-test" "/tmp"; then
        echo "  ✓ 工作目录正确: /tmp"
    else
        LOG_CONTENT=$(kubectl exec -n "$TEST_NS" "$AGENT_POD" -- cat /var/log/fast-sandbox/sb-workdir-test.log 2>/dev/null || echo "log not found")
        echo "  ❌ 工作目录错误. 日志内容: $LOG_CONTENT"
        kubectl delete sandbox sb-workdir-test -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete sandboxpool $POOL_1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    kubectl delete sandbox sb-workdir-test -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # ========================================
    # Case 3: FastPath API - 验证 Envs 和 WorkingDir
    # ========================================
    echo "  === Case 3: FastPath API - 验证 Envs 和 WorkingDir ==="

    CLIENT_BIN="$ROOT_DIR/bin/fsb-ctl"
    if [ ! -f "$CLIENT_BIN" ]; then
        echo "  编译 CLI 工具..."
        cd "$ROOT_DIR" && go build -o bin/fsb-ctl ./cmd/fsb-ctl && cd - >/dev/null
    fi

    CTRL_NS=$(kubectl get deployment fast-sandbox-controller -A -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null || echo "default")

    kubectl port-forward deployment/fast-sandbox-controller -n "$CTRL_NS" 9090:9090 >/dev/null 2>&1 &
    PF_PID=$!
    wait_for_condition "nc -z localhost 9090" 15 "Port-forward ready"

    # 创建配置文件
    CONFIG_FILE="/tmp/fastpath-test-$RANDOM.yaml"
    cat <<EOF > "$CONFIG_FILE"
image: docker.io/library/alpine:latest
pool_ref: $POOL_1
consistency_mode: fast
command: ["/bin/sh", "-c", "echo \"FASTPATH_VAR=\$FASTPATH_VAR\"; echo \"PWD=\$(pwd)\"; sleep 3600"]
working_dir: /app
envs:
  FASTPATH_VAR: hello_from_fastpath
EOF

    echo "  通过 FastPath 创建 Sandbox..."
    OUT=$("$CLIENT_BIN" run "sb-fastpath-env-$RANDOM" -f "$CONFIG_FILE" --namespace="$TEST_NS" 2>&1)
    rm -f "$CONFIG_FILE"

    if echo "$OUT" | grep -q "successfully"; then
        SB_ID=$(echo "$OUT" | grep "ID:" | awk '{print $2}')
        echo "  ✓ FastPath 创建成功: $SB_ID"

        sleep 5  # 等待容器启动

        # 获取分配的 Agent Pod
        SB_AGENT_POD=$(kubectl get sandbox "$SB_ID" -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
        if [ -z "$SB_AGENT_POD" ]; then
            echo "  ❌ 无法获取 Agent Pod"
            kill $PF_PID 2>/dev/null || true
            kubectl delete sandboxpool $POOL_1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            return 1
        fi

        # 验证环境变量
        echo "  验证 FastPath 环境变量..."
        if verify_env_via_log "$SB_AGENT_POD" "$TEST_NS" "$SB_ID" "FASTPATH_VAR" "hello_from_fastpath"; then
            echo "  ✓ FastPath 环境变量正确: hello_from_fastpath"
        else
            LOG_CONTENT=$(kubectl exec -n "$TEST_NS" "$SB_AGENT_POD" -- cat /var/log/fast-sandbox/${SB_ID}.log 2>/dev/null || echo "log not found")
            echo "  ❌ FastPath 环境变量错误. 日志内容: $LOG_CONTENT"
            kill $PF_PID 2>/dev/null || true
            kubectl delete "$SB_ID" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            kubectl delete sandboxpool $POOL_1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            return 1
        fi

        # 验证工作目录
        echo "  验证 FastPath 工作目录..."
        if verify_pwd_via_log "$SB_AGENT_POD" "$TEST_NS" "$SB_ID" "/app"; then
            echo "  ✓ FastPath 工作目录正确: /app"
        else
            echo "  ❌ FastPath 工作目录错误"
            kill $PF_PID 2>/dev/null || true
            kubectl delete "$SB_ID" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            kubectl delete sandboxpool $POOL_1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            return 1
        fi

        kubectl delete sandbox "$SB_ID" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    else
        echo "  ❌ FastPath 调用失败: $OUT"
        kill $PF_PID 2>/dev/null || true
        kubectl delete sandboxpool $POOL_1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    kill $PF_PID 2>/dev/null || true

    # 最终清理
    echo "  清理测试资源..."
    kubectl delete sandboxpool $POOL_1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
