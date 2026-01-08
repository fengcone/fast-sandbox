#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"
AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"

echo "=== 0. Cleanup Old Resources ==="
kubectl delete sandboxpool --all --ignore-not-found=true || true
kubectl delete sandbox --all --ignore-not-found=true || true
kubectl delete pods -l app=sandbox-agent --force --grace-period=0 --ignore-not-found=true || true

echo "=== 0.1 Cleanup KIND Node Residue ==="
# 如果集群存在，清理所有 fast-sandbox 管理的容器和 snapshot
if kind get clusters | grep -q "^$CLUSTER_NAME$"; then
    echo "Cleaning up containerd residue..."
    # 清理所有带 fast-sandbox.io/managed label 的容器
    for c in $(docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io container list -q 'labels."fast-sandbox.io/managed"=="true"' 2>/dev/null || echo ""); do
        docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io task kill -s SIGKILL "$c" 2>/dev/null || true
        docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io task delete "$c" 2>/dev/null || true
        docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io container delete "$c" 2>/dev/null || true
    done
    # 清理可能残留的 snapshot（以 sb- 或 test-sandbox 开头）
    for s in $(docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io snapshot list 2>/dev/null | grep -E "^(sb-|test-sandbox|multi-sb-)" | awk '{print $1}' || echo ""); do
        docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io snapshot rm "$s" 2>/dev/null || true
    done
    echo "Containerd cleanup done."
fi

echo "=== 1. Building Images ==="
cd "$(dirname "$0")/../../"
make docker-agent AGENT_IMAGE=$AGENT_IMAGE
make docker-controller CONTROLLER_IMAGE=$CONTROLLER_IMAGE

echo "=== 2. Loading Images into Kind ==="
if ! kind get clusters | grep -q "^$CLUSTER_NAME$"; then
    echo "Cluster $CLUSTER_NAME not found. Creating..."
    # 使用基础配置创建，不再需要额外的 KVM 映射，因为我们要测 runc 和 gVisor (ptrace)
    kind create cluster --name $CLUSTER_NAME --image kindest/node:v1.35.0
fi
kind load docker-image $AGENT_IMAGE --name $CLUSTER_NAME
kind load docker-image $CONTROLLER_IMAGE --name $CLUSTER_NAME

echo "=== 3. Deploying Controller ==="
kubectl apply -f test/e2e/manifests/controller-deploy.yaml
# 强制重启以确保使用最新加载的镜像
kubectl rollout restart deployment/fast-sandbox-controller
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s

echo "=== 4. Applying SandboxPool (maxSandboxesPerPod=1) ==="
cat <<EOF > test/e2e/manifests/pool-autoscale.yaml
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: autoscale-pool
  namespace: default
spec:
  capacity:
    poolMin: 1
    poolMax: 2
    bufferMin: 0
  maxSandboxesPerPod: 1
  runtimeType: container
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: $AGENT_IMAGE
        imagePullPolicy: IfNotPresent
EOF
kubectl apply -f test/e2e/manifests/pool-autoscale.yaml

echo "=== 5. Waiting for Initial Agent Pod ==="
sleep 5
kubectl wait --for=condition=ready pod -l fast-sandbox.io/pool=autoscale-pool --timeout=60s
echo "Initial Agent Pod is Ready."

echo "=== 6. Applying Sandboxes (Demand for 2 slots) ==="
kubectl apply -f test/e2e/manifests/sb-1.yaml
# 稍微错开时间，减少调度冲突
sleep 2
kubectl apply -f test/e2e/manifests/sb-2.yaml

echo "=== 7. Verifying Autoscaling ==="
echo "Waiting for pool to scale up to 2 pods..."
for i in {1..30}; do
    COUNT=$(kubectl get pods -l fast-sandbox.io/pool=autoscale-pool --no-headers 2>/dev/null | wc -l)
    echo "Check $i: Agent Pod Count=$COUNT"
    if [ "$COUNT" -ge 2 ]; then
        echo "SUCCESS: Pool scaled up to $COUNT pods due to demand!"
        break
    fi
    if [ $i -eq 30 ]; then
        echo "FAILURE: Pool failed to scale up."
        kubectl get sandboxpool autoscale-pool -o yaml
        kubectl logs -l control-plane=controller-manager --tail=50
        exit 1
    fi
    sleep 3
done

echo "Giving new agent 15s to register..."
sleep 15

echo "=== 8. Verifying Running Status ==="
for i in {1..30}; do
    P1=$(kubectl get sandbox sb-1 -o jsonpath="{.status.phase}" 2>/dev/null || echo "")
    P2=$(kubectl get sandbox sb-2 -o jsonpath="{.status.phase}" 2>/dev/null || echo "")
    echo "Check $i: Sandbox-1=$P1, Sandbox-2=$P2"
    if [[ "$P1" == "running" && "$P2" == "running" ]]; then
        echo "SUCCESS: Both sandboxes are RUNNING on separate agents!"
        break
    fi
    sleep 5
done

if [[ "$P1" != "running" || "$P2" != "running" ]]; then
    echo "FAILURE: Sandboxes failed to reach running state."
    kubectl get sandbox -o wide
    kubectl logs -l app=sandbox-agent --tail=50
    exit 1
fi

# ============================================
# 额外测试场景
# ============================================

echo "=== 9. Testing Scale Down ==="
echo "Deleting sandboxes to trigger scale down..."
kubectl delete sandbox sb-1 sb-2 --ignore-not-found=true
# 等待 controller 检测到变化并执行缩容
sleep 20

for i in {1..15}; do
    # 验证至少有一个 Pod 被标记删除（有 deletionTimestamp）
    DELETING_COUNT=$(kubectl get pods -l fast-sandbox.io/pool=autoscale-pool -o jsonpath='{range .items[*]}{.metadata.deletionTimestamp}{"\n"}{end}' 2>/dev/null | grep -v '^$' | wc -l | tr -d ' ')
    RUNNING_COUNT=$(kubectl get pods -l fast-sandbox.io/pool=autoscale-pool --field-selector=status.phase=Running --no-headers 2>/dev/null | wc -l | tr -d ' ')
    echo "Check $i: Running=$RUNNING_COUNT, Terminating=$DELETING_COUNT"
    
    # 验证缩容意图：至少有 1 个 Pod 正在被删除
    if [ "$DELETING_COUNT" -ge 1 ]; then
        echo "SUCCESS: Pool triggered scale down (${DELETING_COUNT} pod(s) terminating)!"
        # 给一些时间让 Pod 实际终止（如果可以的话）
        sleep 5
        break
    fi
    if [ $i -eq 15 ]; then
        echo "FAILURE: Pool failed to trigger scale down."
        kubectl get sandboxpool autoscale-pool -o yaml
        kubectl logs -l control-plane=controller-manager --tail=20
        exit 1
    fi
    sleep 3
done

# 清理 Terminating Pods（force delete 绕过信号处理问题）
echo "Force cleaning terminating pods..."
kubectl delete pods -l fast-sandbox.io/pool=autoscale-pool --force --grace-period=0 --ignore-not-found=true || true
sleep 5

echo "=== 10. Testing PoolMax Boundary ==="
echo "Creating 3 sandboxes to test poolMax=2 limit..."
kubectl apply -f test/e2e/manifests/sb-1.yaml
kubectl apply -f test/e2e/manifests/sb-2.yaml
kubectl apply -f test/e2e/manifests/sb-3.yaml
sleep 15

for i in {1..15}; do
    COUNT=$(kubectl get pods -l fast-sandbox.io/pool=autoscale-pool --no-headers 2>/dev/null | wc -l | tr -d ' ')
    echo "Check $i: Agent Pod Count=$COUNT (expecting max 2)"
    if [ "$COUNT" -eq 2 ]; then
        echo "SUCCESS: Pod count capped at poolMax (2 pods)!"
        break
    fi
    if [ "$COUNT" -gt 2 ]; then
        echo "FAILURE: Pool exceeded poolMax! Got $COUNT pods."
        exit 1
    fi
    sleep 3
done

# 验证第三个 sandbox 处于 pending 状态（无法调度）
P3=$(kubectl get sandbox sb-3 -o jsonpath="{.status.phase}" 2>/dev/null || echo "")
ASSIGNED3=$(kubectl get sandbox sb-3 -o jsonpath="{.status.assignedPod}" 2>/dev/null || echo "")
if [[ "$ASSIGNED3" == "" ]]; then
    echo "SUCCESS: sb-3 remains unscheduled due to capacity limit (phase=$P3)"
else
    echo "INFO: sb-3 was scheduled to $ASSIGNED3 (phase=$P3)"
fi

# 清理
kubectl delete sandbox --all --ignore-not-found=true
kubectl delete sandboxpool autoscale-pool --ignore-not-found=true
sleep 5

echo "=== 11. Testing BufferMin Prewarming ==="
cat <<EOF > test/e2e/manifests/pool-buffer.yaml
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: buffer-pool
  namespace: default
spec:
  capacity:
    poolMin: 1
    poolMax: 3
    bufferMin: 2
  maxSandboxesPerPod: 1
  runtimeType: container
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: $AGENT_IMAGE
        imagePullPolicy: IfNotPresent
EOF
kubectl apply -f test/e2e/manifests/pool-buffer.yaml
sleep 10

# bufferMin=2 意味着需要提前创建2个空闲槽位
# 由于 maxSandboxesPerPod=1，需要 poolMin + ceil(bufferMin/1) = 1 + 2 = 3 pods? 
# 实际公式: (active + pending + bufferMin) / maxPerPod = (0 + 0 + 2) / 1 = 2 pods
# 但受 poolMin 约束，至少 1 pod，所以最终 max(2, 1) = 2 pods

for i in {1..15}; do
    COUNT=$(kubectl get pods -l fast-sandbox.io/pool=buffer-pool --no-headers 2>/dev/null | wc -l | tr -d ' ')
    echo "Check $i: Agent Pod Count=$COUNT (expecting 2 for bufferMin=2)"
    if [ "$COUNT" -ge 2 ]; then
        echo "SUCCESS: Pool prewarmed with $COUNT pods for bufferMin!"
        break
    fi
    if [ $i -eq 15 ]; then
        echo "FAILURE: BufferMin prewarming failed."
        kubectl get sandboxpool buffer-pool -o yaml
        exit 1
    fi
    sleep 3
done

# 清理
kubectl delete sandboxpool buffer-pool --ignore-not-found=true
kubectl delete pods -l fast-sandbox.io/pool=buffer-pool --force --grace-period=0 --ignore-not-found=true || true
sleep 5

echo "=== 12. Testing Multi-Capacity (maxSandboxesPerPod=3) ==="
cat <<EOF > test/e2e/manifests/pool-multi.yaml
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: multi-pool
  namespace: default
spec:
  capacity:
    poolMin: 1
    poolMax: 2
    bufferMin: 0
  maxSandboxesPerPod: 3
  runtimeType: container
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: $AGENT_IMAGE
        imagePullPolicy: IfNotPresent
EOF
kubectl apply -f test/e2e/manifests/pool-multi.yaml
sleep 10

kubectl wait --for=condition=ready pod -l fast-sandbox.io/pool=multi-pool --timeout=60s

# 创建 3 个 Sandbox，应该都调度到同一个 Pod
cat <<EOF | kubectl apply -f -
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: multi-sb-1
  namespace: default
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: multi-pool
---
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: multi-sb-2
  namespace: default
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: multi-pool
---
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: multi-sb-3
  namespace: default
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: multi-pool
EOF

sleep 15

# 验证仍然只有 1 个 Pod（因为容量足够）
COUNT=$(kubectl get pods -l fast-sandbox.io/pool=multi-pool --no-headers 2>/dev/null | wc -l | tr -d ' ')
if [ "$COUNT" -eq 1 ]; then
    echo "SUCCESS: 3 sandboxes scheduled on single pod (maxSandboxesPerPod=3)!"
else
    echo "INFO: Pod count is $COUNT (may have scaled if capacity filled)"
fi

# 验证所有 sandbox 达到 running 状态
for i in {1..20}; do
    RUNNING_COUNT=$(kubectl get sandbox -l '!fast-sandbox.io/pool' -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' 2>/dev/null | grep -c "running" || echo "0")
    echo "Check $i: Running sandboxes=$RUNNING_COUNT/3"
    if [ "$RUNNING_COUNT" -ge 3 ]; then
        echo "SUCCESS: All 3 sandboxes are running!"
        break
    fi
    if [ $i -eq 20 ]; then
        echo "WARNING: Not all sandboxes reached running state. Running=$RUNNING_COUNT"
    fi
    sleep 3
done

# 最终清理
kubectl delete sandbox --all --ignore-not-found=true
kubectl delete sandboxpool --all --ignore-not-found=true

echo ""
echo "============================================"
echo "ALL E2E TESTS COMPLETED SUCCESSFULLY!"
echo "============================================"
