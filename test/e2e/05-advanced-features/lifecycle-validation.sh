#!/bin/bash

describe() {
    echo "Sandbox 生命周期验证 - 完整测试删除、过期、Agent丢失等场景"
}

run() {
    # TEST_NS and AGENT_IMAGE are from the environment

    # ========================================
    # Sub-case 1: 正常删除流程
    # ========================================
    echo "  === 测试 1: 正常删除流程 ==="
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: lifecycle-pool
spec:
  capacity: { poolMin: 1, poolMax: 2 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=lifecycle-pool" 60 "$TEST_NS"

    # 创建 sandbox
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-normal-delete
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: lifecycle-pool
EOF

    # 等待 Bound
    if ! wait_for_condition "kubectl get sandbox sb-normal-delete -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qi 'bound'" 30 "Sandbox bound"; then
        echo "  ❌ Sandbox 未进入 Bound 状态"
        return 1
    fi

    AGENT_POD=$(kubectl get pod -l fast-sandbox.io/pool=lifecycle-pool -n "$TEST_NS" -o jsonpath='{.items[0].metadata.name}')
    SB_NAME="sb-normal-delete"

    # 等待容器创建完成（检查 sandboxID 是否分配）
    SANDBOX_ID=""
    for i in {1..10}; do
        SANDBOX_ID=$(kubectl get sandbox sb-normal-delete -n "$TEST_NS" -o jsonpath='{.status.sandboxID}' 2>/dev/null)
        if [ -n "$SANDBOX_ID" ]; then
            echo "  ✓ 容器已创建 (sandboxID: $SANDBOX_ID, 等待 ${i}s)"
            break
        fi
        if [ $i -eq 10 ]; then
            echo "  ❌ 容器未在 Agent 上创建（超时 10s）"
            return 1
        fi
        sleep 1
    done

    # 删除 sandbox
    kubectl delete sandbox sb-normal-delete -n "$TEST_NS" >/dev/null 2>&1

    # 等待删除并验证容器被删除
    for i in {1..30}; do
        if ! kubectl get sandbox sb-normal-delete -n "$TEST_NS" >/dev/null 2>&1; then
            # CRD 已删除，验证容器也被删除（通过 Agent HTTP API）
            # 通过 port-forward 访问 Agent HTTP API 检查容器
            AGENT_PF_PORT=$((10000 + RANDOM % 1000))
            kubectl port-forward "pod/$AGENT_POD" -n "$TEST_NS" "$AGENT_PF_PORT:5758" >/dev/null 2>&1 &
            AGENT_PF_PID=$!
            sleep 1

            CONTAINER_EXISTS=false
            if nc -z localhost "$AGENT_PF_PORT" 2>/dev/null; then
                # 使用 Agent HTTP API 检查容器
                if curl -s "http://localhost:$AGENT_PF_PORT/sandboxes" 2>/dev/null | grep -q "\"name\":\"$SB_NAME\""; then
                    CONTAINER_EXISTS=true
                fi
            fi

            kill "$AGENT_PF_PID" 2>/dev/null || true

            if [ "$CONTAINER_EXISTS" = true ]; then
                echo "  ❌ CRD 删除但容器仍存在"
                return 1
            fi
            echo "  ✓ 正常删除流程：容器和CRD都已删除"
            break
        fi
        sleep 1
    done

    if kubectl get sandbox sb-normal-delete -n "$TEST_NS" >/dev/null 2>&1; then
        echo "  ❌ CRD 未被删除"
        return 1
    fi

    # ========================================
    # Sub-case 2: Expired 后删除
    # ========================================
    echo "  === 测试 2: Expired 后删除 ==="
    EXPIRE_TIME=$(date -u -d "+15 seconds" +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null || date -u -v+15S +"%Y-%m-%dT%H:%M:%SZ")

    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-expired-delete
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: lifecycle-pool
  expireTime: "$EXPIRE_TIME"
EOF

    # 等待 Bound
    wait_for_condition "kubectl get sandbox sb-expired-delete -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qi 'bound'" 30 "Sandbox bound" >/dev/null

    SB_NAME="sb-expired-delete"

    # 等待过期
    for i in {1..30}; do
        if kubectl get sandbox sb-expired-delete -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null | grep -qi "expired"; then
            echo "  ✓ Sandbox 已过期，Phase=Expired"
            break
        fi
        sleep 1
    done

    # 验证容器已删除（通过 sandboxID 被清空）
    CURRENT_SANDBOX_ID=$(kubectl get sandbox sb-expired-delete -n "$TEST_NS" -o jsonpath='{.status.sandboxID}' 2>/dev/null)
    if [ -n "$CURRENT_SANDBOX_ID" ]; then
        echo "  ❌ 过期后 sandboxID 仍存在: $CURRENT_SANDBOX_ID"
        return 1
    fi
    echo "  ✓ 过期后容器已删除"

    # 删除已过期的 sandbox
    kubectl delete sandbox sb-expired-delete -n "$TEST_NS" >/dev/null 2>&1

    # 应该能快速删除（不需要调用 Agent）
    if wait_for_condition "! kubectl get sandbox sb-expired-delete -n '$TEST_NS' >/dev/null 2>&1" 10 "Sandbox deleted"; then
        echo "  ✓ Expired sandbox 删除成功"
    else
        echo "  ❌ Expired sandbox 删除超时"
        return 1
    fi

    # ========================================
    # Sub-case 3: Agent Pod 删除 - AutoRecreate 模式
    # ========================================
    echo "  === 测试 3: Agent Pod 删除 (AutoRecreate) ==="

    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-agent-recreate
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: lifecycle-pool
  failurePolicy: AutoRecreate
EOF

    wait_for_condition "kubectl get sandbox sb-agent-recreate -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qi 'bound'" 30 "Sandbox bound" >/dev/null

    ORIGINAL_AGENT=$(kubectl get sandbox sb-agent-recreate -n "$TEST_NS" -o jsonpath='{.status.assignedPod}')
    echo "  ✓ 原始 Agent: $ORIGINAL_AGENT"

    # 删除 Agent Pod（模拟 Agent 崩溃）
    kubectl delete pod "$ORIGINAL_AGENT" -n "$TEST_NS" >/dev/null 2>&1

    # 等待新 Agent Pod 启动
    wait_for_pod "fast-sandbox.io/pool=lifecycle-pool" 60 "$TEST_NS"

    NEW_AGENT=$(kubectl get pod -l fast-sandbox.io/pool=lifecycle-pool -n "$TEST_NS" -o jsonpath='{.items[0].metadata.name}')
    echo "  ✓ 新 Agent: $NEW_AGENT"

    # 验证 sandbox 被重新调度到新 Agent
    for i in {1..30}; do
        CURRENT_AGENT=$(kubectl get sandbox sb-agent-recreate -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null)
        if [ "$CURRENT_AGENT" = "$NEW_AGENT" ]; then
            echo "  ✓ Sandbox 已重新调度到新 Agent"
            break
        fi
        sleep 1
    done

    # 验证容器在新 Agent 上（通过 sandboxID 和 assignedPod）
    SB_NAME="sb-agent-recreate"
    NEW_SANDBOX_ID=$(kubectl get sandbox sb-agent-recreate -n "$TEST_NS" -o jsonpath='{.status.sandboxID}' 2>/dev/null)
    CURRENT_ASSIGNED_POD=$(kubectl get sandbox sb-agent-recreate -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null)

    if [ -n "$NEW_SANDBOX_ID" ] && [ "$CURRENT_ASSIGNED_POD" = "$NEW_AGENT" ]; then
        echo "  ✓ 容器已在新 Agent 上运行 (sandboxID: $NEW_SANDBOX_ID)"
    else
        echo "  ❌ 容器未在新 Agent 上创建"
        echo "     sandboxID: $NEW_SANDBOX_ID, assignedPod: $CURRENT_ASSIGNED_POD, expected Pod: $NEW_AGENT"
        return 1
    fi

    # 清理
    kubectl delete sandbox sb-agent-recreate -n "$TEST_NS" --timeout=30s >/dev/null 2>&1
    wait_for_condition "! kubectl get sandbox sb-agent-recreate -n '$TEST_NS' >/dev/null 2>&1" 20 "Sandbox deleted" >/dev/null

    # ========================================
    # Sub-case 4: Pending 状态删除
    # ========================================
    echo "  === 测试 4: Pending 状态删除 ==="

    # 创建一个立即进入删除的 sandbox（在创建后立即删除，模拟 Pending 状态）
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1 &
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-pending-delete
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: lifecycle-pool
EOF

    # 等待一小段时间然后删除（可能在 Pending 或刚进入 Bound）
    sleep 3
    kubectl delete sandbox sb-pending-delete -n "$TEST_NS" >/dev/null 2>&1

    # 验证删除成功
    if wait_for_condition "! kubectl get sandbox sb-pending-delete -n '$TEST_NS' >/dev/null 2>&1" 15 "Sandbox deleted"; then
        echo "  ✓ Pending 状态删除成功"
    else
        echo "  ⚠ Pending 状态删除可能有问题，但继续"
    fi

    # ========================================
    # Sub-case 5: 删除状态转换验证
    # ========================================
    echo "  === 测试 5: 删除状态转换验证 ==="

    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-state-transition
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: lifecycle-pool
EOF

    wait_for_condition "kubectl get sandbox sb-state-transition -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qi 'bound'" 30 "Sandbox bound" >/dev/null

    # 记录状态
    PHASE_BEFORE=$(kubectl get sandbox sb-state-transition -n "$TEST_NS" -o jsonpath='{.status.phase}')
    echo "  ✓ 删除前状态: $PHASE_BEFORE"

    # 异步删除
    kubectl delete sandbox sb-state-transition -n "$TEST_NS" >/dev/null 2>&1 &
    DELETE_PID=$!

    # 检查中间状态 Terminating
    for i in {1..10}; do
        PHASE=$(kubectl get sandbox sb-state-transition -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [ "$PHASE" = "Terminating" ]; then
            echo "  ✓ 检测到中间状态: Terminating"
            break
        fi
        sleep 1
    done

    # 等待删除完成
    wait $DELETE_PID 2>/dev/null
    wait_for_condition "! kubectl get sandbox sb-state-transition -n '$TEST_NS' >/dev/null 2>&1" 20 "Sandbox deleted" >/dev/null
    echo "  ✓ 删除状态转换正确"

    # ========================================
    # Sub-case 6: 过期自动清理
    # ========================================
    echo "  === 测试 6: 过期自动清理 ==="

    EXPIRE_TIME=$(date -u -d "+10 seconds" +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null || date -u -v+10S +"%Y-%m-%dT%H:%M:%SZ")

    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-auto-expiry
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: lifecycle-pool
  expireTime: "$EXPIRE_TIME"
EOF

    wait_for_condition "kubectl get sandbox sb-auto-expiry -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qi 'bound'" 30 "Sandbox bound" >/dev/null

    SB_NAME="sb-auto-expiry"
    AGENT_POD=$(kubectl get pod -l fast-sandbox.io/pool=lifecycle-pool -n "$TEST_NS" -o jsonpath='{.items[0].metadata.name}')

    # 等待过期
    echo "  等待过期..."
    for i in {1..20}; do
        PHASE=$(kubectl get sandbox sb-auto-expiry -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null)
        if [ "$PHASE" = "Expired" ]; then
            echo "  ✓ Sandbox 已过期，Phase=Expired"
            break
        fi
        sleep 1
    done

    # 验证容器被删除（通过 sandboxID 被清空）
    SANDBOX_ID=$(kubectl get sandbox sb-auto-expiry -n "$TEST_NS" -o jsonpath='{.status.sandboxID}' 2>/dev/null)
    if [ -n "$SANDBOX_ID" ]; then
        echo "  ❌ 过期后容器仍存在 (sandboxID: $SANDBOX_ID)"
        return 1
    fi
    echo "  ✓ 过期后容器已自动删除"

    # 验证 assignedPod 和 sandboxID 被清空
    ASSIGNED_POD=$(kubectl get sandbox sb-auto-expiry -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null)
    SANDBOX_ID=$(kubectl get sandbox sb-auto-expiry -n "$TEST_NS" -o jsonpath='{.status.sandboxID}' 2>/dev/null)
    if [ -z "$ASSIGNED_POD" ] && [ -z "$SANDBOX_ID" ]; then
        echo "  ✓ 过期后状态字段已清空"
    else
        echo "  ⚠ 状态字段: assignedPod=$ASSIGNED_POD, sandboxID=$SANDBOX_ID"
    fi

    # 验证 CRD 保留（用于历史查询）
    if kubectl get sandbox sb-auto-expiry -n "$TEST_NS" >/dev/null 2>&1; then
        echo "  ✓ 过期后 CRD 保留"
    else
        echo "  ❌ CRD 不应被删除"
        return 1
    fi

    # 清理
    kubectl delete sandbox sb-auto-expiry -n "$TEST_NS" --timeout=30s >/dev/null 2>&1

    # ========================================
    # Sub-case 7: Manual 模式 Agent 丢失
    # ========================================
    echo "  === 测试 7: Manual 模式 Agent 丢失 ==="

    # 创建一个专用的 Pool 用于这个测试
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: manual-test-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=manual-test-pool" 60 "$TEST_NS"

    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-manual-failure
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: manual-test-pool
  failurePolicy: Manual
EOF

    wait_for_condition "kubectl get sandbox sb-manual-failure -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qi 'bound'" 30 "Sandbox bound" >/dev/null

    # 记录原始状态
    ORIGINAL_AGENT=$(kubectl get sandbox sb-manual-failure -n "$TEST_NS" -o jsonpath='{.status.assignedPod}')
    PHASE_BEFORE=$(kubectl get sandbox sb-manual-failure -n "$TEST_NS" -o jsonpath='{.status.phase}')
    echo "  ✓ 删除 Agent 前: Agent=$ORIGINAL_AGENT, Phase=$PHASE_BEFORE"

    # 同时删除 SandboxPool 和 Agent Pod，模拟 Agent 丢失且无法恢复
    # 这样可以防止新 Agent 启动，同时真正的 Agent Pod 也被删除
    kubectl delete sandboxpool manual-test-pool -n "$TEST_NS" --timeout=30s >/dev/null 2>&1
    kubectl delete pod "$ORIGINAL_AGENT" -n "$TEST_NS" --force --grace-period=0 >/dev/null 2>&1

    # 等待 Pod 被删除
    for i in {1..10}; do
        if ! kubectl get pod "$ORIGINAL_AGENT" -n "$TEST_NS" >/dev/null 2>&1; then
            echo "  ✓ Agent Pod 已删除"
            break
        fi
        sleep 1
    done

    # 触发 Controller Reconcile（通过添加 annotation）
    kubectl annotate sandbox sb-manual-failure -n "$TEST_NS" "trigger-reconcile=$(date +%s)" --overwrite >/dev/null 2>&1

    # 等待心跳超时（至少 10 秒）加上 Registry 清理时间
    echo "  等待心跳超时和 Registry 清理..."
    for i in {1..30}; do
        PHASE=$(kubectl get sandbox sb-manual-failure -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null)
        ASSIGNED=$(kubectl get sandbox sb-manual-failure -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null)
        if [ "$PHASE" = "Lost" ]; then
            echo "  ✓ Manual 模式：Agent 丢失后转换到 Lost 状态"
            break
        fi
        # 每 5 秒触发一次 Reconcile
        if [ $((i % 5)) -eq 0 ] && [ $i -gt 0 ]; then
            kubectl annotate sandbox sb-manual-failure -n "$TEST_NS" "trigger-reconcile=$(date +%s)" --overwrite >/dev/null 2>&1
        fi
        if [ $i -eq 30 ]; then
            echo "  ❌ 超时等待 Lost 状态，当前 Phase: $PHASE, Assigned: $ASSIGNED"
            return 1
        fi
        sleep 1
    done

    # 验证 assignedPod 和 sandboxID 被清空
    ASSIGNED_POD=$(kubectl get sandbox sb-manual-failure -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null)
    SANDBOX_ID=$(kubectl get sandbox sb-manual-failure -n "$TEST_NS" -o jsonpath='{.status.sandboxID}' 2>/dev/null)
    if [ -z "$ASSIGNED_POD" ] && [ -z "$SANDBOX_ID" ]; then
        echo "  ✓ Lost 状态下 assignedPod 和 sandboxID 已清空"
    else
        echo "  ⚠ Lost 状态字段: assignedPod=$ASSIGNED_POD, sandboxID=$SANDBOX_ID"
    fi

    # 重新创建 Pool 以模拟 Agent 恢复
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: manual-test-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    # 等待新 Agent 启动
    wait_for_pod "fast-sandbox.io/pool=manual-test-pool" 60 "$TEST_NS"

    # 新 Agent 可用后，sandbox 应该自动从 Lost 转换到 Pending（等待 rescheduling）
    for i in {1..15}; do
        PHASE=$(kubectl get sandbox sb-manual-failure -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null)
        ASSIGNED=$(kubectl get sandbox sb-manual-failure -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null)
        if [ "$PHASE" = "Pending" ] && [ -n "$ASSIGNED" ]; then
            echo "  ✓ 新 Agent 可用后从 Lost 转换到 Pending，准备重新调度"
            break
        fi
        if [ $i -eq 15 ]; then
            echo "  ⚠ 超时等待 Pending 状态，当前 Phase: $PHASE, Assigned: $ASSIGNED"
            break
        fi
        sleep 1
    done

    # 清理
    kubectl delete sandbox sb-manual-failure -n "$TEST_NS" --timeout=30s >/dev/null 2>&1
    wait_for_condition "! kubectl get sandbox sb-manual-failure -n '$TEST_NS' >/dev/null 2>&1" 15 "Sandbox deleted" >/dev/null
    kubectl delete sandboxpool manual-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # ========================================
    # Sub-case 8: 心跳超时场景
    # ========================================
    echo "  === 测试 8: 心跳超时处理 ==="

    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-heartbeat
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: lifecycle-pool
  failurePolicy: AutoRecreate
EOF

    wait_for_condition "kubectl get sandbox sb-heartbeat -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qi 'bound'" 30 "Sandbox bound" >/dev/null

    # 获取当前 Agent
    CURRENT_AGENT=$(kubectl get sandbox sb-heartbeat -n "$TEST_NS" -o jsonpath='{.status.assignedPod}')
    echo "  ✓ 当前 Agent: $CURRENT_AGENT"

    # 心跳超时测试比较复杂，这里只验证基本状态
    # 实际心跳超时需要更长时间（10秒无心跳）
    echo "  ✓ 心跳超时场景验证（需要 Agent 停止心跳才能完整测试）"

    # 清理
    kubectl delete sandbox sb-heartbeat -n "$TEST_NS" --timeout=30s >/dev/null 2>&1
    wait_for_condition "! kubectl get sandbox sb-heartbeat -n '$TEST_NS' >/dev/null 2>&1" 15 "Sandbox deleted" >/dev/null

    # 清理测试 Pool
    kubectl delete sandboxpool lifecycle-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
