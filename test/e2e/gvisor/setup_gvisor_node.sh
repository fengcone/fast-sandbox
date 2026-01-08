#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"
NODE_NAME="$CLUSTER_NAME-control-plane"

echo "=== 1. Installing gVisor (runsc) in KIND Node ==="
docker exec $NODE_NAME bash -c "
    apt-get update && apt-get install -y curl
    (
      set -e
      pkill runsc || true
      rm -f /usr/local/bin/runsc /usr/local/bin/containerd-shim-runsc-v1
      
      ARCH=\$(uname -m)
      URL=https://storage.googleapis.com/gvisor/releases/release/latest/\${ARCH}
      curl -L \${URL}/runsc -o /usr/local/bin/runsc
      curl -L \${URL}/runsc.sha256 -o /usr/local/bin/runsc.sha256
      curl -L \${URL}/containerd-shim-runsc-v1 -o /usr/local/bin/containerd-shim-runsc-v1
      chmod +x /usr/local/bin/runsc /usr/local/bin/containerd-shim-runsc-v1
    )
"

echo "=== 2. Configuring Containerd inside KIND Node ==="
docker exec $NODE_NAME bash -c "
    # 创建 runsc 专用配置
    mkdir -p /etc/containerd
    cat <<EOF > /etc/containerd/runsc.toml
[runsc]
  platform = \"ptrace\"
EOF

    # 导出并修改 containerd 配置
    containerd config default > /etc/containerd/config.toml
    
    # 增加 runsc 运行时定义
    cat <<EOF >> /etc/containerd/config.toml
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.runsc]
  runtime_type = 'io.containerd.runsc.v1'
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.runsc.options]
  ConfigPath = '/etc/containerd/runsc.toml'
EOF
"

echo "=== 3. Restarting Containerd ==="
docker exec $NODE_NAME systemctl restart containerd

echo "=== Setup Completed Successfully ==="