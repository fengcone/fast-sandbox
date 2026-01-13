#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

# 测试命名空间
TEST_NS="e2e-$(basename "$SCRIPT_DIR")"

setup_suite() {
    cleanup_test_resources "$TEST_NS"
    echo "=== [Setup] 初始化测试环境 ==="
    kubectl create namespace "$TEST_NS" 2>/dev/null || true
    setup_env "controller agent"
    install_infra
}

run_test_suite "$SCRIPT_DIR" "$1" "setup_suite"
