#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"
NODE_NAME="$CLUSTER_NAME-control-plane"

# 版本定义
FC_VERSION="v1.7.0"
SHIM_VERSION="0.3.0"

echo "=== 1. Checking KVM access inside KIND ==="
if ! docker exec $NODE_NAME ls /dev/kvm > /dev/null 2>&1; then
    echo "ERROR: /dev/kvm not found in KIND node. Please recreate KIND with /dev/kvm mapping."
    exit 1
fi

echo "=== 2. Installing Firecracker and Shim in KIND Node ==="
docker exec $NODE_NAME bash -c "
    apt-get update && apt-get install -y curl
    # Install Firecracker
    curl -L https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-$(uname -m) -o /usr/local/bin/firecracker
    chmod +x /usr/local/bin/firecracker
    
    # Install containerd-shim-firecracker
    curl -L https://github.com/firecracker-microvm/firecracker-containerd/releases/download/v${SHIM_VERSION}/containerd-shim-firecracker-v1-linux-amd64 -o /usr/local/bin/containerd-shim-firecracker-v1
    chmod +x /usr/local/bin/containerd-shim-firecracker-v1
"

echo "=== 3. Configuring Containerd inside KIND Node ==="
docker exec $NODE_NAME bash -c "
    cat <<EOF >> /etc/containerd/config.toml
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.firecracker]
  runtime_type = 'io.containerd.firecracker.v1'
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.firecracker.options]
  BinaryPath = '/usr/local/bin/firecracker'
  ConfigPath = '/etc/containerd/firecracker-runtime.json'
EOF

    # 创建必要的 runtime 配置
    cat <<EOF > /etc/containerd/firecracker-runtime.json
{
  "kernel_image_path": "/var/lib/firecracker/vmlinux",
  "default_network_interfaces": [{
    "allow_mmds": true
  }]
}
EOF
"

echo "=== 4. Downloading Kernel Image ==="
# 这里使用一个公开的微型内核镜像
docker exec $NODE_NAME mkdir -p /var/lib/firecracker
docker exec $NODE_NAME curl -L https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/kernels/vmlinux.bin -o /var/lib/firecracker/vmlinux

echo "=== 5. Restarting Containerd in KIND Node ==="
docker exec $NODE_NAME systemctl restart containerd

echo "=== Setup Completed Successfully ==="
