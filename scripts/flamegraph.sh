#!/bin/bash
# Generate flamegraph from CPU profile

echo "Generating flamegraph..."

if [ ! -f /tmp/controller_cpu.prof ]; then
    echo "No profile found at /tmp/controller_cpu.prof"
    echo "Run profiling first with: go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30 > /tmp/controller_cpu.prof"
    exit 1
fi

go tool pprof -http=:8080 /tmp/controller_cpu.prof
