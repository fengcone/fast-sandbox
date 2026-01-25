#!/bin/bash

# Case 1: Fast-Path 一致性模式与孤儿清理测试
describe() {
    echo "Fast-Path 一致性模式 - 验证 Fast/Strong 两种模式、孤儿清理及端口隔离"
}

run() {
    CLIENT_BIN="$ROOT_DIR/bin/fsb-ctl"
    if [ ! -f "$CLIENT_BIN" ]; then
        echo "  编译官方 CLI 工具..."
        cd "$ROOT_DIR" && go build -o bin/fsb-ctl ./cmd/fsb-ctl && cd - >/dev/null
    fi

    CTRL_NS=$(kubectl get deployment fast-sandbox-controller -A -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null || echo "default")
    IMAGE="docker.io/library/alpine:latest"

    # ========================================
    # Sub-case 1: Fast 模式 - 端口隔离验证
    # ========================================
    echo "  === Sub-case 1: Fast 模式 - 端口隔离验证 ==="
    POOL_1="fast-path-pool-$RANDOM"
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

    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-crd-a }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: $POOL_1
  exposedPorts: [8080]
EOF
    wait_for_condition "kubectl get sandbox sb-crd-a -n $TEST_NS -o jsonpath='{.status.assignedPod}' 2>/dev/null | grep -q '.'" 30 "SB-A Assigned"

    kubectl port-forward deployment/fast-sandbox-controller -n "$CTRL_NS" 9090:9090 >/dev/null 2>&1 &
    PF_PID=$!
    wait_for_condition "nc -z localhost 9090" 15 "Port-forward ready"

    echo "  通过 Fast-Path (Fast 模式) 创建 Sandbox B (端口 5758)..."
    # 使用新参数 --name，添加默认命令 /bin/sleep 3600
    OUT=$("$CLIENT_BIN" run "sb-fast-$RANDOM" --image="$IMAGE" --pool="$POOL_1" --ports=5758 --namespace="$TEST_NS" /bin/sleep 3600 2>&1)
    if echo "$OUT" | grep -q "successfully"; then
        SB_B=$(echo "$OUT" | grep "ID:" | awk '{print $2}')
        echo "  ✓ Fast-Path 创建成功: $SB_B"
        
        # 验证 List 功能
        if "$CLIENT_BIN" list --namespace="$TEST_NS" | grep -q "$SB_B"; then
            echo "  ✓ Sandbox 在 list 中显示"
        else
            echo "  ❌ Sandbox 未在 list 中显示"; kill $PF_PID; return 1
        fi

        if kubectl get sandbox sb-crd-a -n "$TEST_NS" >/dev/null 2>&1; then
            echo "  ✓ Sandbox A 仍然存在"
        else
            echo "  ❌ Sandbox A 丢失"; kill $PF_PID; return 1
        fi
    else
        echo "  ❌ Fast-Path 调用失败: $OUT"; kill $PF_PID; return 1
    fi
    kill $PF_PID 2>/dev/null || true
    kubectl delete sandboxpool $POOL_1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # ========================================
    # Sub-case 2: Strong 模式验证
    # ========================================
    echo "  === Sub-case 2: Strong 模式验证 ==="
    POOL_2="strong-pool-$RANDOM"
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: $POOL_2 }
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
    wait_for_pod "fast-sandbox.io/pool=$POOL_2" 60 "$TEST_NS"

    kubectl port-forward deployment/fast-sandbox-controller -n "$CTRL_NS" 9090:9090 >/dev/null 2>&1 &
    PF_PID=$!
    wait_for_condition "nc -z localhost 9090" 15 "Port-forward ready"

    echo "  通过 Fast-Path (Strong 模式) 创建 Sandbox..."
    OUT=$("$CLIENT_BIN" run "sb-strong-$RANDOM" --image="$IMAGE" --pool="$POOL_2" --mode=strong --namespace="$TEST_NS" /bin/sleep 3600 2>&1)
    if echo "$OUT" | grep -q "successfully"; then
        SB_ID=$(echo "$OUT" | grep "ID:" | awk '{print $2}')
        sleep 5
        PHASE=$(kubectl get sandbox "$SB_ID" -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [ "$PHASE" = "Bound" ] || [ "$PHASE" = "Running" ] || [ "$PHASE" = "Pending" ]; then
            echo "  ✓ Strong 模式状态正确: $PHASE"
        else
            echo "  ❌ Strong 模式状态错误: '$PHASE'"; kill $PF_PID; return 1
        fi
    else
        echo "  ❌ Strong 模式调用失败: $OUT"; kill $PF_PID; return 1
    fi
    kill $PF_PID 2>/dev/null || true
    kubectl delete sandboxpool $POOL_2 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # ========================================
    # Sub-case 3: Fast 模式孤儿清理 (ValidatingWebhook)
    # ========================================
    echo "  === Sub-case 3: Fast 模式孤儿清理 (Webhook 模拟失败) ==="
    POOL_3="orphan-pool-$RANDOM"
    if [ -f "$SCRIPT_DIR/scripts/setup_webhook.sh" ]; then
        echo "  部署故障注入 Webhook..."
        export TEST_NS
        bash "$SCRIPT_DIR/scripts/setup_webhook.sh"
        
        cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: $POOL_3 }
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
        wait_for_pod "fast-sandbox.io/pool=$POOL_3" 60 "$TEST_NS"

        kubectl port-forward deployment/fast-sandbox-controller -n "$CTRL_NS" 9090:9090 >/dev/null 2>&1 &
        PF_PID=$!
        wait_for_condition "nc -z localhost 9090" 15 "Port-forward ready"

        ORPHAN_NAME="test-orphan-$(date +%s)"
        echo "  创建故意失败的沙箱: $ORPHAN_NAME"
        # 使用 --name 指定特定名称，添加默认命令
        OUT=$("$CLIENT_BIN" run "$ORPHAN_NAME" --image="$IMAGE" --pool="$POOL_3" --namespace="$TEST_NS" /bin/sleep 3600 2>&1)
        
        if echo "$OUT" | grep -q "successfully"; then
            echo "  ✓ Fast-Path 调用成功 (正如预期)"
            NODE_NAME=$(kubectl get pod -l fast-sandbox.io/pool=$POOL_3 -n "$TEST_NS" -o jsonpath='{.items[0].spec.nodeName}')
            CONTAINER_ID=$(docker exec "$NODE_NAME" ctr -n k8s.io containers ls | grep "$ORPHAN_NAME" | awk '{print $1}')
            if [ -n "$CONTAINER_ID" ]; then
                echo "  ✓ 发现孤儿容器: $CONTAINER_ID"
                echo "  等待 Janitor 扫描清理..."
                local found=0
                for i in {1..25}; do
                    if ! docker exec "$NODE_NAME" ctr -n k8s.io containers ls | grep -q "$CONTAINER_ID"; then
                        echo "  🎉 SUCCESS: Janitor 清理了孤儿容器!"
                        found=1; break
                    fi
                    echo "  Check $i: 容器仍在运行..."
                    sleep 5
                done
                [ $found -eq 0 ] && (echo "  ❌ Janitor 清理超时"; kill $PF_PID; return 1)
            else
                echo "  ❌ 宿主机未发现容器"; kill $PF_PID; return 1
            fi
        else
            echo "  ❌ Fast-Path 调用报错: $OUT"; kill $PF_PID; return 1
        fi
        kill $PF_PID 2>/dev/null || true
        bash "$SCRIPT_DIR/scripts/cleanup_webhook.sh"
    fi

    return 0
}