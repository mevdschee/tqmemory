#!/bin/bash

# TQMemory Memcached Compatibility Tests
# Downloads and runs the official Memcached Perl test suite

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
TEST_DIR="$SCRIPT_DIR/memcached_tests"
PORT="${TQMEMORY_PORT:-11299}"
BINARY="$SCRIPT_DIR/tqmemory_test"
SERVER_PID=""

# Test files to download from official memcached repo
MEMCACHED_RAW="https://raw.githubusercontent.com/memcached/memcached/master"
TEST_FILES=(
    "t/lib/MemcachedTest.pm"
    "t/getset.t"
    "t/cas.t"
    "t/incrdecr.t"
    "t/touch.t"
    "t/flush-all.t"
    "t/noreply.t"
    "t/flags.t"
    "t/expirations.t"
)

cleanup() {
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        echo "Stopping TQMemory (PID: $SERVER_PID)..."
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    if [ -f "$BINARY" ]; then
        rm -f "$BINARY"
    fi
}

trap cleanup EXIT INT TERM

download_tests() {
    echo "=== Downloading Memcached Test Suite ==="
    mkdir -p "$TEST_DIR/t/lib"
    
    for file in "${TEST_FILES[@]}"; do
        local dest="$TEST_DIR/$file"
        local url="$MEMCACHED_RAW/$file"
        
        if [ ! -f "$dest" ]; then
            echo "Downloading $file..."
            mkdir -p "$(dirname "$dest")"
            curl -sL "$url" -o "$dest"
        fi
    done
    echo ""
}

patch_test_library() {
    echo "=== Patching MemcachedTest.pm for TQMemory ==="
    local pm_file="$TEST_DIR/t/lib/MemcachedTest.pm"
    
    # Check if already patched
    if grep -q "TQMEMORY_PATCHED" "$pm_file" 2>/dev/null; then
        echo "Already patched"
        echo ""
        return
    fi
    
    # Create backup
    cp "$pm_file" "$pm_file.orig"
    
    # Patch 1: Use our binary via environment variable
    sed -i 's|sub get_memcached_exe {|sub get_memcached_exe {\n    # TQMEMORY_PATCHED\n    return $ENV{TQMEMORY_BINARY} if $ENV{TQMEMORY_BINARY};|' "$pm_file"
    
    # Patch 2: Remove -u root option (TQMemory doesn't need it)
    sed -i 's|\$args .= " -u root";|# $args .= " -u root"; # Disabled for TQMemory|' "$pm_file"
    
    # Patch 3: Remove -o relaxed_privileges (TQMemory doesn't support -o)
    sed -i 's|\$args .= " -o relaxed_privileges";|# $args .= " -o relaxed_privileges"; # Disabled for TQMemory|' "$pm_file"
    
    # Patch 4: Remove UDP port option (TQMemory doesn't support UDP)
    sed -i 's|\$args .= " -U \$udpport";|# $args .= " -U $udpport"; # Disabled for TQMemory|' "$pm_file"
    
    # Patch 5: Remove SSL options (TQMemory doesn't support SSL yet)
    sed -i 's|\$args .= " -Z";|# $args .= " -Z"; # Disabled for TQMemory|' "$pm_file"
    sed -i 's|\$args .= " -o ssl_chain_cert=\$server_crt";|# Disabled for TQMemory|' "$pm_file"
    sed -i 's|\$args .= " -o ssl_key=\$server_key";|# Disabled for TQMemory|' "$pm_file"
    
    # Patch 6: Remove timedrun wrapper (we don't have it)
    sed -i 's|my \$cmd = "\$builddir/timedrun 600 \$valgrind \$exe \$args";|my $cmd = "$exe $args";|' "$pm_file"
    
    echo "Patched MemcachedTest.pm"
    echo ""
}

build_tqmemory() {
    echo "=== Building TQMemory ==="
    go build -o "$BINARY" "$PROJECT_DIR/cmd/tqmemory"
    echo "Built: $BINARY"
    echo ""
}

run_tests() {
    echo "=== Running Memcached Compatibility Tests ==="
    echo ""
    
    cd "$TEST_DIR/t"
    
    # Set environment for tests
    export TQMEMORY_BINARY="$BINARY"
    export MEMCACHED_PORT="$PORT"
    
    # Run each test file
    local passed=0
    local failed=0
    local total=0
    
    for test_file in *.t; do
        if [ -f "$test_file" ]; then
            echo "--- Running $test_file ---"
            total=$((total + 1))
            
            if perl "$test_file" 2>&1; then
                passed=$((passed + 1))
                echo "PASSED: $test_file"
            else
                failed=$((failed + 1))
                echo "FAILED: $test_file"
            fi
            echo ""
        fi
    done
    
    echo "=== Test Summary ==="
    echo "Total: $total, Passed: $passed, Failed: $failed"
    
    if [ $failed -gt 0 ]; then
        exit 1
    fi
}

# Main
echo "=== TQMemory Memcached Test Suite ==="
echo ""

# Check dependencies
if ! command -v perl &> /dev/null; then
    echo "ERROR: perl is required but not installed"
    exit 1
fi

if ! perl -e 'use Test::More' 2>/dev/null; then
    echo "ERROR: Perl Test::More module is required"
    echo "Install with: cpan Test::More"
    exit 1
fi

download_tests
patch_test_library
build_tqmemory
run_tests

echo ""
echo "=== ALL TESTS COMPLETED ==="
