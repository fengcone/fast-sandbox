#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"
NODE_NAME="$CLUSTER_NAME-control-plane"

# 获取架构
ARCH=$(docker exec $NODE_NAME uname -m)
case $ARCH in
    x86_64)
        FC_ARCH="x86_64"
        SHIM_ARCH="amd64"
        KERNEL_URL="https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/kernels/vmlinux.bin"
        ;;
    aarch64)
        FC_ARCH="aarch64"
        SHIM_ARCH="arm64"
        KERNEL_URL="https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/aarch64/kernels/vmlinux.bin"
        ;;
    *)
        echo "Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

echo "Detected Architecture: $ARCH"

# 版本定义
FC_VERSION="v1.7.0"
SHIM_VERSION="0.3.0"

echo "=== 1. Checking KVM access inside KIND ==="
if ! docker exec $NODE_NAME ls /dev/kvm > /dev/null 2>&1; then
    echo "WARNING: /dev/kvm not found. Firecracker will likely fail to start tasks."
fi

echo "=== 2. Installing Firecracker, Shim and Devmapper Tools ==="
docker exec $NODE_NAME bash -c "
    apt-get update && apt-get install -y curl dmsetup lvm2
    
    # Install Firecracker
    curl -L https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-${ARCH} -o /usr/local/bin/firecracker
    chmod +x /usr/local/bin/firecracker
    
    # Install containerd-shim-firecracker
    curl -L https://github.com/firecracker-microvm/firecracker-containerd/releases/download/v${SHIM_VERSION}/containerd-shim-firecracker-v1-linux-${SHIM_ARCH} -o /usr/local/bin/containerd-shim-firecracker-v1
    chmod +x /usr/local/bin/containerd-shim-firecracker-v1
"

echo "=== 3. Downloading Kernel ==="
docker exec $NODE_NAME mkdir -p /var/lib/firecracker
docker exec $NODE_NAME curl -L ${KERNEL_URL} -o /var/lib/firecracker/vmlinux

echo "=== 4. Setting up Devmapper in KIND Node ==="
# 这一步非常关键：在容器内创建 Loop Device 并配置 Thin Pool
docker exec --privileged $NODE_NAME bash -c "
    # 1. 创建稀疏文件 (10GB)
    mkdir -p /var/lib/containerd/devmapper
    truncate -s 10G /var/lib/containerd/devmapper/data.img
    
    # 2. 挂载 Loop Device
    # 需要先确保 /dev/loop-control 存在
    if [ ! -e /dev/loop-control ]; then
        mknod /dev/loop-control c 10 237
    fi
    
    # 查找空闲 loop 设备
    LOOPDEV=\$(losetup -f)
    if [ -z "\$LOOPDEV" ]; then
        # 如果没有 loop 设备，手动创建几个
        for i in {0..7}; do
            if [ ! -e /dev/loop\$i ]; then mknod /dev/loop\$i b 7 \$i; fi
        done
        LOOPDEV=\$(losetup -f)
    fi
    
    losetup \$LOOPDEV /var/lib/containerd/devmapper/data.img
    
    # 3. 创建 Thin Pool
    # 这里的参数计算：10GB / 512字节 = 20971520扇区
    dmsetup create fc-pool --table \"0 20971520 thin-pool \$LOOPDEV \$LOOPDEV 128 32768 1 skip_block_zeroing\" || true
"

echo "=== 5. Configuring Containerd with Devmapper & Firecracker ==="
docker exec $NODE_NAME bash -c "
    # 清理旧配置
    # 注意：这里我们覆盖整个 plugins 配置区以确保结构正确
    
    cat <<EOF > /etc/containerd/config.toml
version = 2

[plugins]
  [plugins."io.containerd.grpc.v1.cri"]
    [plugins."io.containerd.grpc.v1.cri".containerd]
      snapshotter = "devmapper"
      
      [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
        runtime_type = "io.containerd.runc.v2"

      [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.firecracker]
        runtime_type = "io.containerd.firecracker.v1"
        [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.firecracker.options]
          BinaryPath = "/usr/local/bin/firecracker"
          ConfigPath = "/etc/containerd/firecracker-runtime.json"

  [plugins."io.containerd.snapshotter.v1.devmapper"]
    pool_name = "fc-pool"
    root_path = "/var/lib/containerd/devmapper"
    base_image_size = "2GB"
    discard_blocks = true
EOF

    cat <<EOF > /etc/containerd/firecracker-runtime.json
{
  "kernel_image_path": "/var/lib/firecracker/vmlinux",
  "default_network_interfaces": [],
  "ht_enabled": false
}
EOF
"

echo "=== 6. Restarting Containerd in KIND Node ==="
docker exec $NODE_NAME systemctl restart containerd

echo "=== Setup Completed Successfully ==="
