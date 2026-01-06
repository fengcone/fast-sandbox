#!/bin/bash
set -e

VM_NAME="docker-rootful"

echo "=== 1. Checking VM Status ==="
if ! limactl list | grep $VM_NAME | grep -q "Running"; then
    echo "VM $VM_NAME is not running. Please start it first."
    exit 1
fi

echo "=== 2. Installing Dependencies inside Lima ==="
limactl shell $VM_NAME sudo bash -c '
    apt-get update
    apt-get install -y dmsetup lvm2 curl make conntrack
    
    # 加载必要内核模块
    modprobe dm_thin_pool
    echo "dm_thin_pool" >> /etc/modules
'

echo "=== 3. Setting up Devmapper Thin Pool ==="
limactl shell $VM_NAME sudo bash -c '
    # 只有当设备不存在时才创建
    if [ ! -f /var/lib/containerd-devmapper.img ]; then
        echo "Creating 10GB sparse file for devmapper..."
        truncate -s 10G /var/lib/containerd-devmapper.img
        
        # 挂载 Loop 设备
        LOOPDEV=$(losetup --find --show /var/lib/containerd-devmapper.img)
        
        # 创建 Thin Pool
        # 10GB = 20971520 sectors
        dmsetup create fc-pool --table "0 20971520 thin-pool $LOOPDEV $LOOPDEV 128 32768 1 skip_block_zeroing"
        echo "Devmapper pool created."
    else
        echo "Devmapper image already exists. Skipping."
    fi
'

echo "=== 4. Installing Firecracker & Shim ==="
limactl shell $VM_NAME sudo bash -c '
    FC_VERSION="v1.7.0"
    SHIM_VERSION="0.4.0" # 使用更新的 Shim 版本
    ARCH="x86_64"

    # Firecracker
    if [ ! -f /usr/local/bin/firecracker ]; then
        echo "Installing Firecracker..."
        curl -L https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-${ARCH}.tgz | tar -xz
        mv release-${FC_VERSION}-${ARCH}/firecracker-${FC_VERSION}-${ARCH} /usr/local/bin/firecracker
        chmod +x /usr/local/bin/firecracker
        rm -rf release-${FC_VERSION}-${ARCH}
    fi

    # Shim
    if [ ! -f /usr/local/bin/containerd-shim-firecracker-v1 ]; then
        echo "Installing Shim..."
        curl -L https://github.com/firecracker-microvm/firecracker-containerd/releases/download/v${SHIM_VERSION}/containerd-shim-firecracker-v1-v${SHIM_VERSION}-linux-amd64.tar.gz | tar -xz
        mv containerd-shim-firecracker-v1 /usr/local/bin/
        chmod +x /usr/local/bin/containerd-shim-firecracker-v1
    fi
'

echo "=== 5. Installing Minikube & Kubectl ==="
limactl shell $VM_NAME sudo bash -c '
    if ! command -v minikube &> /dev/null; then
        curl -LO https://storage.googleapis.com/minikube/releases/latest/minikube-linux-amd64
        install minikube-linux-amd64 /usr/local/bin/minikube
        rm minikube-linux-amd64
    fi
    
    if ! command -v kubectl &> /dev/null; then
        curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
        install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl
    fi
'

echo "=== 6. Configuring Containerd ==="
limactl shell $VM_NAME sudo bash -c '
    mkdir -p /etc/containerd
    # 备份
    if [ ! -f /etc/containerd/config.toml.bak ]; then
        cp /etc/containerd/config.toml /etc/containerd/config.toml.bak || true
    fi

    # 生成配置 (这里我们追加配置，假设默认配置已存在，或者覆盖)
    # 为了保险，我们直接覆盖为包含 devmapper 的配置
    containerd config default > /etc/containerd/config.toml
    
    # 注入 Devmapper 和 Firecracker 配置
    # 注意：这里用 sed 简单插入，或者直接追加
    cat <<EOF >> /etc/containerd/config.toml

[plugins."io.containerd.snapshotter.v1.devmapper"]
  pool_name = "fc-pool"
  root_path = "/var/lib/containerd/devmapper"
  base_image_size = "10GB"
  discard_blocks = true

[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.firecracker]
  runtime_type = "io.containerd.firecracker.v1"
EOF

    systemctl restart containerd
'

echo "=== 7. Starting Minikube (Bare Metal Mode) ==="
limactl shell $VM_NAME sudo bash -c '
    # 使用 none 驱动，直接利用宿主机 Containerd
    # 注意：需要指定 --cri-socket 因为我们可能同时有 docker 和 containerd
    minikube start --driver=none --container-runtime=containerd
'

echo "=== Environment Setup Complete! ==="
echo "You can now run 'limactl shell $VM_NAME' to access the environment."
