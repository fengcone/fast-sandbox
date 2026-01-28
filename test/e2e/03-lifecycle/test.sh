#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

run_test_suite "$SCRIPT_DIR" "$1" "setup_lifecycle_suite"

setup_lifecycle_suite() {
    echo "=== Setting up Lifecycle Test Suite ==="
    # Lifecycle 测试不需要特殊的 setup
}
