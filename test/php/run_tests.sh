#!/bin/bash

# PHP tests for TQMemory
# Requires: php-memcached extension

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
PORT="${TQMEMORY_PORT:-11212}"
BINARY="$SCRIPT_DIR/tqmemory_test"
SERVER_PID=""

cleanup() {
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        echo "Stopping TQMemory (PID: $SERVER_PID)..."
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    if [ -f "$BINARY" ]; then
        echo "Removing binary..."
        rm -f "$BINARY"
    fi
}

trap cleanup EXIT INT TERM

echo "=== TQMemory PHP Tests ==="
echo ""

# Build the server
echo "Building TQMemory..."
go build -o "$BINARY" "$PROJECT_DIR/cmd/tqmemory"
echo "Built: $BINARY"
echo ""

# Start the server
echo "Starting TQMemory on port $PORT..."
"$BINARY" -p "$PORT" &
SERVER_PID=$!
sleep 1

# Check if server started
if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "ERROR: TQMemory failed to start"
    exit 1
fi
echo "TQMemory running (PID: $SERVER_PID)"
echo ""

# Run simple socket test
echo "--- Simple Socket Test ---"
php "$SCRIPT_DIR/simple_socket.php"
echo ""

# Run compatibility tests
echo "--- Compatibility Tests ---"
TQMEMORY_PORT="$PORT" php "$SCRIPT_DIR/compatibility_test.php"
echo ""

# Run session tests
echo "--- Session Tests ---"
TQMEMORY_PORT="$PORT" php "$SCRIPT_DIR/session_test.php"
echo ""

echo "=== ALL PHP TESTS PASSED ==="
