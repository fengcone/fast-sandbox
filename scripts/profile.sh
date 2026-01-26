#!/bin/bash
# Run controller with CPU profiling

echo "Starting controller with profiling enabled..."
echo "Profiler will be available at http://localhost:6060/debug/pprof/"

go run cmd/controller/main.go &
CONTROLLER_PID=$!

echo "Controller PID: $CONTROLLER_PID"
echo "Press Ctrl+C to stop"

wait $CONTROLLER_PID
