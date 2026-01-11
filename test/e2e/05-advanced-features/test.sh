#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

# 测试命名空间
TEST_NS="e2e-$(basename "$SCRIPT_DIR")"

ENV_INITIALIZED=false

setup_once() {
    if [ "$ENV_INITIALIZED" = "false" ]; then
        # 先清理旧资源
        cleanup_test_resources "$TEST_NS"

        echo "=== [Setup] 初始化测试环境 ==="

        # 创建测试命名空间
        kubectl create namespace "$TEST_NS" 2>/dev/null || true

        setup_env "controller agent"
        install_infra
        ENV_INITIALIZED=true
    fi
}

run_case() {
    local case_file=$1
    local case_name=$(basename "$case_file" .sh)

    # 清理之前 case 可能遗留的函数
    unset -f describe precondition run 2>/dev/null || true

    echo ""
    echo "========================================"
    echo "📋 Case: $case_name"
    source "$case_file"

    if declare -f describe > /dev/null; then
        echo "📝 $(describe)"
    fi

    if declare -f precondition > /dev/null; then
        if ! precondition; then
            echo "⏭️  跳过 (前置条件不满足)"
            unset -f describe precondition run 2>/dev/null || true
            return 0
        fi
    fi

    if declare -f run > /dev/null; then
        if run; then
            echo "✅ PASSED: $case_name"
            PASSED+=("$case_name")
        else
            echo "❌ FAILED: $case_name"
            FAILED+=("$case_name")
        fi
    fi

    # 清理当前 case 的函数
    unset -f describe precondition run 2>/dev/null || true
}

cleanup_and_report() {
    echo ""
    echo "========================================"
    echo "📊 测试结果汇总"
    echo "----------------------------------------"
    echo "✅ 通过: ${#PASSED[@]}"
    echo "❌ 失败: ${#FAILED[@]}"

    if [ ${#FAILED[@]} -gt 0 ]; then
        echo ""
        echo "失败的测试:"
        for name in "${FAILED[@]}"; do
            echo "  - $name"
        done
        return 1
    fi
}

trap cleanup_and_report EXIT

main() {
    echo "🚀 E2E 测试套件: $(basename "$SCRIPT_DIR")"
    echo "========================================"

    setup_once

    for case in "${SCRIPT_DIR}"/*.sh; do
        local case_name=$(basename "$case")
        # 跳过 test.sh 本身
        if [ "$case_name" = "test.sh" ]; then
            continue
        fi
        if [ -f "$case" ]; then
            run_case "$case"
        fi
    done
}

PASSED=()
FAILED=()

main "$@"
