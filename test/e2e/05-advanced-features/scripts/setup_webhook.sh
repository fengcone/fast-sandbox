#!/bin/bash

# ValidatingWebhook 配置 - 用于测试孤儿清理场景
# 拒绝名称为 test-orphan-* 的 Sandbox CRD 创建

set -e

# 预期命名空间
WEBHOOK_NS=${TEST_NS:-"e2e-webhook-test"}

echo "=== 部署 ValidatingWebhook 用于孤儿测试 ==="

# 1. 创建命名空间
kubectl create namespace "$WEBHOOK_NS" 2>/dev/null || true

# 2. 编译 webhook server (使用静态编译)
WEBHOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "编译 webhook server..."
cd "$WEBHOOK_DIR/webhook"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o webhook-server main.go
if [ ! -f webhook-server ]; then
    echo "❌ 编译失败，请检查 Go 环境"
    exit 1
fi

# 3. 创建包含 webhook 二进制的 ConfigMap
kubectl create configmap webhook-binary --from-file=webhook-server -n "$WEBHOOK_NS" --dry-run=client -o yaml | kubectl apply -f -

# 4. 使用自签名证书创建 TLS Secret
# 生成 CA 和服务器证书
openssl req -x509 -newkey rsa:2048 -keyout ca.key -out ca.crt -days 1 -nodes -subj "/CN=webhook-ca" 2>/dev/null
cat > csr.conf <<EOF
[req]
req_extensions = v3_req
distinguished_name = req_distinguished_name
[req_distinguished_name]
[v3_req]
basicConstraints = CA:FALSE
keyUsage = nonRepudiation, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names
[alt_names]
DNS.1 = sandbox-webhook
DNS.2 = sandbox-webhook.$WEBHOOK_NS
DNS.3 = sandbox-webhook.$WEBHOOK_NS.svc
DNS.4 = sandbox-webhook.$WEBHOOK_NS.svc.cluster.local
EOF

openssl genrsa -out server.key 2048 2>/dev/null
openssl req -new -key server.key -out server.csr -subj "/CN=sandbox-webhook" -config csr.conf 2>/dev/null
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out server.crt -days 1 -extensions v3_req -extfile csr.conf 2>/dev/null

# 创建 TLS Secret
kubectl create secret tls webhook-tls --cert=server.crt --key=server.key -n "$WEBHOOK_NS" --dry-run=client -o yaml | kubectl apply -f -

# 5. 创建 webhook deployment
cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sandbox-webhook
  namespace: $WEBHOOK_NS
spec:
  replicas: 1
  selector:
    matchLabels:
      app: sandbox-webhook
  template:
    metadata:
      labels:
        app: sandbox-webhook
    spec:
      containers:
      - name: webhook
        image: alpine:latest
        command: ["/bin/sh", "-c"]
        args:
          - |
            # 从 ConfigMap 复制二进制文件
            cp /config/webhook-server /tmp/webhook-server
            chmod +x /tmp/webhook-server
            # 复制证书
            cp /certs/tls.crt /tmp/server.crt
            cp /certs/tls.key /tmp/server.key
            # 启动 webhook
            /tmp/webhook-server
        env:
        - name: PORT
          value: "443"
        - name: REJECT_PATTERN
          value: "test-orphan-"
        volumeMounts:
        - name: webhook-binary
          mountPath: /config
        - name: certs
          mountPath: /certs
          readOnly: true
      volumes:
      - name: webhook-binary
        configMap:
          name: webhook-binary
          defaultMode: 0555
      - name: certs
        secret:
          secretName: webhook-tls
EOF

# 6. 创建 Service
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Service
metadata:
  name: sandbox-webhook
  namespace: $WEBHOOK_NS
spec:
  ports:
  - port: 443
    targetPort: 443
    name: https
  selector:
    app: sandbox-webhook
EOF

# 7. 等待 webhook Pod 就绪
echo "等待 webhook Pod 就绪..."
kubectl wait --for=condition=ready pod -l app=sandbox-webhook -n "$WEBHOOK_NS" --timeout=60s

# 8. 获取 CA Bundle
CA_BUNDLE=$(cat ca.crt | base64 | tr -d '\n')

# 9. 创建 ValidatingWebhookConfiguration
cat <<EOF | kubectl apply -f -
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: sandbox-orphan-test-webhook
  annotations:
    admissionregistration.kubernetes.io/ignore-webhook-namespace-config: "true"
webhooks:
- name: sandbox.validator.fast.io
  rules:
  - operations: ["CREATE"]
    apiGroups: ["sandbox.fast.io"]
    apiVersions: ["v1alpha1"]
    resources: ["sandboxes"]
    scope: "*"
  clientConfig:
    service:
      namespace: $WEBHOOK_NS
      name: sandbox-webhook
      path: /validate
    caBundle: $CA_BUNDLE
  admissionReviewVersions: ["v1"]
  sideEffects: None
  failurePolicy: Fail
  timeoutSeconds: 5
EOF

# 清理临时文件
rm -f ca.key ca.crt ca.srl server.key server.csr server.crt csr.conf webhook-server

echo "✓ ValidatingWebhook 部署完成"
echo ""
echo "注意: 此 webhook 会拒绝所有名称以 'test-orphan-' 开头的 Sandbox CRD"
echo ""
