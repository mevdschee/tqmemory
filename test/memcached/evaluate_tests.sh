#!/bin/bash

# TQMemory Test Evaluator
# Runs each .t test file individually and captures detailed results

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
TEST_DIR="$SCRIPT_DIR/perl/t"
BINARY="$SCRIPT_DIR/tqmemory_test"
RESULTS_DIR="$SCRIPT_DIR/results"

cleanup() {
    pkill -9 -f tqmemory_test 2>/dev/null || true
}

trap cleanup EXIT INT TERM

# Build TQMemory
echo "=== Building TQMemory ==="
go build -o "$BINARY" "$PROJECT_DIR/cmd/tqmemory" || exit 1
echo "Built: $BINARY"
echo ""

# Create results directory
mkdir -p "$RESULTS_DIR"
rm -f "$RESULTS_DIR"/*.txt

# Kill any existing processes
cleanup
sleep 1

echo "=== Running Individual Tests ==="
echo ""

cd "$TEST_DIR"
export TQMEMORY_BINARY="$BINARY"

# Summary counters
total_tests=0
total_pass=0
total_fail=0

for test_file in *.t; do
    if [ ! -f "$test_file" ]; then
        continue
    fi
    
    test_name="${test_file%.t}"
    result_file="$RESULTS_DIR/${test_name}.txt"
    
    echo -n "Testing $test_file... "
    
    # Run test with timeout and capture output
    timeout 30 perl "$test_file" > "$result_file" 2>&1 || true
    
    # Count results using grep -c (returns 0 if no match, which is fine)
    pass=$(grep -c "^ok " "$result_file" 2>/dev/null) || pass=0
    fail=$(grep -c "^not ok " "$result_file" 2>/dev/null) || fail=0
    
    total_tests=$((total_tests + 1))
    total_pass=$((total_pass + pass))
    total_fail=$((total_fail + fail))
    
    if [ "$fail" -eq 0 ] && [ "$pass" -gt 0 ]; then
        echo "PASS ($pass tests)"
    elif [ "$pass" -gt 0 ] || [ "$fail" -gt 0 ]; then
        echo "PARTIAL (pass: $pass, fail: $fail)"
    else
        echo "ERROR (no test output)"
    fi
    
    # Cleanup between tests
    pkill -9 -f tqmemory_test 2>/dev/null || true
    sleep 0.5
done

echo ""
echo "=== Summary ==="
echo "Total test files: $total_tests"
echo "Total assertions - Passed: $total_pass, Failed: $total_fail"
echo ""
echo "Detailed results saved in: $RESULTS_DIR/"
echo ""

# Show failures for each test
echo "=== Failure Details ==="
for result_file in "$RESULTS_DIR"/*.txt; do
    test_name=$(basename "$result_file" .txt)
    failures=$(grep "^not ok " "$result_file" 2>/dev/null | head -5) || true
    
    if [ -n "$failures" ]; then
        echo ""
        echo "$test_name.t failures:"
        echo "$failures" | sed 's/^/  /'
        # Show first failure detail
        grep -A3 "Failed test" "$result_file" 2>/dev/null | head -6 | sed 's/^/    /' || true
    fi
done

echo ""
echo "=== Done ==="
