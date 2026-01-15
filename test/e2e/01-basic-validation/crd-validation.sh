#!/bin/bash

describe() {
    echo "CRD 必填字段验证 - 验证 API Server 拒绝无效的 Sandbox 请求"
}

run() {
    local result

    # 测试1: 缺少 image 字段
    result=$(kubectl apply -f - -n "$TEST_NS" 2>&1 <<EOF
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: test-no-image
spec:
  poolRef: test-pool
EOF
    )
    if ! echo "$result" | grep -qiE "required|invalid"; then
        echo "  ❌ 缺少 image 字段未被拒绝, result: $result"
        return 1
    fi
    echo "  ✓ 缺少 image 字段被正确拒绝"

    # 测试2: 缺少 poolRef 字段
    result=$(kubectl apply -f - -n "$TEST_NS" 2>&1 <<EOF
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: test-no-poolref
spec:
  image: nginx:alpine
EOF
    )
    if ! echo "$result" | grep -qiE "required|invalid"; then
        echo "  ❌ 缺少 poolRef 字段未被拒绝, result: $result"
        return 1
    fi
    echo "  ✓ 缺少 poolRef 字段被正确拒绝"

    # 测试3: 空 image 字段
    result=$(kubectl apply -f - -n "$TEST_NS" 2>&1 <<EOF
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: test-empty-image
spec:
  image: ""
  poolRef: test-pool
EOF
    )
    if ! echo "$result" | grep -qiE "chars long|minLength|less than|invalid"; then
        echo "  ❌ 空 image 字段未被拒绝: $result"
        return 1
    fi
    echo "  ✓ 空 image 字段被正确拒绝"

    # 测试4: 空 poolRef 字段
    result=$(kubectl apply -f - -n "$TEST_NS" 2>&1 <<EOF
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: test-empty-poolref
spec:
  image: nginx:alpine
  poolRef: ""
EOF
    )
    if ! echo "$result" | grep -qiE "chars long|minLength|less than|invalid"; then
        echo "  ❌ 空 poolRef 字段未被拒绝: $result"
        return 1
    fi
    echo "  ✓ 空 poolRef 字段被正确拒绝"

    # 测试5: 无效的 failurePolicy
    result=$(kubectl apply -f - -n "$TEST_NS" 2>&1 <<EOF
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: test-invalid-failure-policy
spec:
  image: nginx:alpine
  poolRef: test-pool
  failurePolicy: "InvalidPolicy"
EOF
    )
    if ! echo "$result" | grep -qiE "not supported|enum|invalid"; then
        echo "  ❌ 无效的 failurePolicy 未被拒绝: $result"
        return 1
    fi
    echo "  ✓ 无效的 failurePolicy 被正确拒绝"

    # 测试6: envs 缺少 name 字段
    result=$(kubectl apply -f - -n "$TEST_NS" 2>&1 <<EOF
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: test-env-no-name
spec:
  image: nginx:alpine
  poolRef: test-pool
  envs:
    - value: "test-value"
EOF
    )
    if ! echo "$result" | grep -qiE "required|invalid"; then
        echo "  ❌ envs 缺少 name 字段未被拒绝: $result"
        return 1
    fi
    echo "  ✓ envs 缺少 name 字段被正确拒绝"

    # 测试7: 有效的 Sandbox 可以创建 (不需要 poolRef 存在，只需要验证通过)
    result=$(kubectl apply -f - -n "$TEST_NS" 2>&1 <<EOF
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: test-valid-sandbox
spec:
  image: nginx:alpine
  poolRef: test-pool
  exposedPorts: [8080]
  failurePolicy: Manual
  recoveryTimeoutSeconds: 60
EOF
    )
    # 检查是否有验证错误
    if echo "$result" | grep -qiE "invalid|required"; then
        echo "  ❌ 有效的 Sandbox 被拒绝: $result"
        return 1
    fi
    # 清理
    kubectl delete sandbox test-valid-sandbox -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    echo "  ✓ 有效的 Sandbox 成功创建"

    return 0
}
