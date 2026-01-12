#!/bin/bash

# 清理 ValidatingWebhook 测试资源

set -e

# 预期命名空间
WEBHOOK_NS="e2e-webhook-isolated"

echo "=== 清理 ValidatingWebhook 测试资源 ==="

# 1. 删除 ValidatingWebhookConfiguration
kubectl delete validatingwebhookconfiguration sandbox-orphan-test-webhook --ignore-not-found=true 2>/dev/null || true

# 2. 删除命名空间（级联删除所有资源）
kubectl delete namespace "$WEBHOOK_NS" --ignore-not-found=true --timeout=30s 2>/dev/null || true

# 3. 清理本地编译产物
WEBHOOK_DIR_LOCAL="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
rm -f "$WEBHOOK_DIR_LOCAL/../webhook/webhook-server"

echo "✓ ValidatingWebhook 清理完成"
