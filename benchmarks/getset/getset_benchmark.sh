#!/bin/bash
set -e

# TQMemory vs Memcached Benchmark
# Tests with varying thread counts to find optimal configuration

# Helper to kill process on a port
kill_port() {
    PORT=$1
    if command -v lsof >/dev/null; then
        PIDS=$(lsof -t -i:$PORT || true)
        if [ ! -z "$PIDS" ]; then
            echo "Force killing process(es) on port $PORT: $PIDS"
            kill -9 $PIDS 2>/dev/null || true
        fi
    fi
}

# Cleanup function
cleanup() {
    echo "Stopping servers..."
    if [ ! -z "$TQ_PID" ]; then kill $TQ_PID 2>/dev/null || true; fi
    if [ ! -z "$MEM_PID" ]; then kill $MEM_PID 2>/dev/null || true; fi
    
    # Force cleanup ports
    kill_port 11221
    kill_port 11222

    rm -f max_rss.tmp cpu_start.tmp cpu_time.tmp results.tmp
    rm -f tqmemory-server benchmark-tool
}
trap cleanup EXIT

# Check dependencies
if ! command -v memcached &> /dev/null; then
    echo "Error: memcached is not installed or not in PATH."
    exit 1
fi

# Build
echo "Building TQMemory and Benchmark Tool..."
go build -o tqmemory-server ../../cmd/tqmemory
go build -o benchmark-tool .

# Benchmark Configuration
CLIENTS=10
REQUESTS=100000
SIZE=10240
KEYS=100000

# Get CPU time (user + system) from /proc/PID/stat in jiffies
get_cpu_time() {
    PID=$1
    if [ -f /proc/$PID/stat ]; then
        # Fields 14 and 15 are utime and stime in jiffies
        awk '{print $14 + $15}' /proc/$PID/stat 2>/dev/null || echo 0
    else
        echo 0
    fi
}

# Function to monitor max RSS of a PID and track CPU time
start_monitor() {
    PID=$1
    echo 0 > max_rss.tmp
    # Record start CPU time and wall clock
    get_cpu_time $PID > cpu_start.tmp
    echo $(date +%s%N) >> cpu_start.tmp
    (
        while true; do
            if ! kill -0 $PID 2>/dev/null; then break; fi
            rss=$(ps -o rss= -p $PID 2>/dev/null | tr -d ' ' || echo 0)
            if [ -z "$rss" ]; then rss=0; fi
            
            cur_max=$(cat max_rss.tmp)
            if [ "$rss" -gt "$cur_max" ]; then
                echo $rss > max_rss.tmp
            fi
            sleep 0.1
        done
    ) &
    MONITOR_PID=$!
}

stop_monitor() {
    MONITOR_TARGET_PID=$1
    if [ ! -z "$MONITOR_PID" ]; then
        kill $MONITOR_PID 2>/dev/null || true
        wait $MONITOR_PID 2>/dev/null || true
    fi
    
    # Get max memory
    MAX_KB=$(cat max_rss.tmp)
    MAX_MB=$((MAX_KB / 1024))
    
    # Calculate CPU percentage
    CPU_START=$(head -1 cpu_start.tmp)
    TIME_START=$(tail -1 cpu_start.tmp)
    CPU_END=$(get_cpu_time $MONITOR_TARGET_PID)
    TIME_END=$(date +%s%N)
    
    # CPU time in jiffies (typically 100 Hz = 10ms per jiffy)
    CPU_JIFFIES=$((CPU_END - CPU_START))
    # Wall time in nanoseconds, convert to centiseconds (100ths of a second, same as jiffies at 100Hz)
    WALL_NS=$((TIME_END - TIME_START))
    WALL_CS=$((WALL_NS / 10000000))
    
    if [ "$WALL_CS" -gt 0 ]; then
        # CPU percentage = (cpu_jiffies / wall_centiseconds) * 100
        CPU_PCT=$((CPU_JIFFIES * 100 / WALL_CS))
    else
        CPU_PCT=0
    fi
    
    echo "$MAX_MB,$CPU_PCT"
}

run_benchmark() {
    THREAD_COUNT=$1
    MEM_THREADS=$2
    REQ_COUNT=$3

    echo "==========================================================="
    echo "Running Benchmark (TQMemory threads: $THREAD_COUNT, Memcached threads: $MEM_THREADS)"
    echo "==========================================================="

    # Ensure ports are free
    kill_port 11221
    kill_port 11222

    # --- Start TQMemory ---
    echo "Starting TQMemory (Threads: $THREAD_COUNT)..."
    ./tqmemory-server -p 11221 -t $THREAD_COUNT -m 64 > /dev/null 2>&1 &
    TQ_PID=$!

    # --- Start Memcached ---
    echo "Starting Memcached (Threads: $MEM_THREADS)..."
    memcached -p 11222 -m 64 -t $MEM_THREADS -u $(whoami) -c 100000 > /dev/null 2>&1 &
    MEM_PID=$!

    # Wait for startup
    sleep 2

    # --- Run Benchmarks ---

    # TQMemory Binary Protocol
    echo "Benchmarking TQMemory..."
    start_monitor $TQ_PID
    ./benchmark-tool -host localhost:11221 -protocol memc-bin -label "TQMemory" -mode "memory" -clients $CLIENTS -size $SIZE -requests $REQ_COUNT -csv > results.tmp
    STATS=$(stop_monitor $TQ_PID)
    awk -v stats="$STATS" -v threads="$THREAD_COUNT" '{print threads "," $0 "," stats}' results.tmp >> $OUTPUT

    # Memcached
    echo "Benchmarking Memcached..."
    start_monitor $MEM_PID
    ./benchmark-tool -host localhost:11222 -protocol memc-bin -label "Memcached" -mode "memory" -clients $CLIENTS -size $SIZE -requests $REQ_COUNT -csv > results.tmp
    STATS=$(stop_monitor $MEM_PID)
    awk -v stats="$STATS" -v threads="$MEM_THREADS" '{print threads "," $0 "," stats}' results.tmp >> $OUTPUT

    # Cleanup processes for next round
    kill $TQ_PID 2>/dev/null || true
    kill $MEM_PID 2>/dev/null || true
    wait $TQ_PID 2>/dev/null || true
    wait $MEM_PID 2>/dev/null || true
}

generate_visualization() {
python3 << 'EOF'
import pandas as pd
import matplotlib.pyplot as plt
import numpy as np

def annotate_bars(ax):
    for p in ax.patches:
        if p.get_height() > 0:
            ax.annotate(f'{int(p.get_height())}', 
                        (p.get_x() + p.get_width() / 2., p.get_height()), 
                        ha='center', va='bottom', fontsize=8, rotation=90, xytext=(0, 5), 
                        textcoords='offset points')

# Load data
df = pd.read_csv('getset_benchmark.csv')
df.columns = [c.strip() for c in df.columns]
for col in ['Backend', 'Protocol', 'Operation']:
    if col in df.columns:
        df[col] = df[col].astype(str).str.strip()

# --- Figure 1: Performance by Thread Count ---
fig1, ((ax1, ax2), (ax3, ax4)) = plt.subplots(2, 2, figsize=(14, 10))

# SET Performance by thread count
set_df = df[df['Operation'] == 'SET']
set_pivot = set_df.pivot(index='Threads', columns='Backend', values='RPS')
set_pivot.plot(kind='bar', ax=ax1, width=0.8, rot=0)
ax1.set_title('SET Performance by Thread Count')
ax1.set_ylabel('Requests Per Second (RPS)')
ax1.set_xlabel('Threads')
ax1.grid(axis='y', linestyle='--', alpha=0.7)
ax1.legend(title='Backend', loc='upper left')
annotate_bars(ax1)

# GET Performance by thread count
get_df = df[df['Operation'] == 'GET']
get_pivot = get_df.pivot(index='Threads', columns='Backend', values='RPS')
get_pivot.plot(kind='bar', ax=ax2, width=0.8, rot=0, legend=False)
ax2.set_title('GET Performance by Thread Count')
ax2.set_ylabel('Requests Per Second (RPS)')
ax2.set_xlabel('Threads')
ax2.grid(axis='y', linestyle='--', alpha=0.7)
annotate_bars(ax2)

# Memory Usage by thread count (using SET data)
mem_pivot = set_df.pivot(index='Threads', columns='Backend', values='MaxMemory(MB)')
mem_pivot.plot(kind='bar', ax=ax3, width=0.8, rot=0, legend=False)
ax3.set_title('Peak Memory Usage')
ax3.set_ylabel('Megabytes (MB)')
ax3.set_xlabel('Threads')
ax3.grid(axis='y', linestyle='--', alpha=0.7)
annotate_bars(ax3)

# CPU Usage by thread count (using SET data)
cpu_pivot = set_df.pivot(index='Threads', columns='Backend', values='CPU(%)')
cpu_pivot.plot(kind='bar', ax=ax4, width=0.8, rot=0, legend=False)
ax4.set_title('CPU Usage')
ax4.set_ylabel('CPU (%)')
ax4.set_xlabel('Threads')
ax4.grid(axis='y', linestyle='--', alpha=0.7)
annotate_bars(ax4)

# Increase y-limit to fit vertical labels
for ax in (ax1, ax2, ax3, ax4):
    ylim = ax.get_ylim()
    ax.set_ylim(0, ylim[1] * 1.15)

plt.suptitle('TQMemory vs Memcached Performance Benchmark', fontsize=16)
plt.tight_layout(rect=[0, 0.03, 1, 0.95])
plt.savefig('getset_benchmark.png', dpi=150, bbox_inches='tight')
print("Saved: getset_benchmark.png")

# --- Figure 2: Performance Improvement Percentage ---
fig2, (ax5, ax6) = plt.subplots(1, 2, figsize=(12, 5))

# Calculate improvement percentages
threads = set_pivot.index.tolist()
if 'TQMemory' in set_pivot.columns and 'Memcached' in set_pivot.columns:
    set_improvement = ((set_pivot['TQMemory'] - set_pivot['Memcached']) / set_pivot['Memcached'] * 100).values
    get_improvement = ((get_pivot['TQMemory'] - get_pivot['Memcached']) / get_pivot['Memcached'] * 100).values
    
    colors = ['green' if x > 0 else 'red' for x in set_improvement]
    ax5.bar(range(len(threads)), set_improvement, color=colors, tick_label=threads)
    ax5.axhline(y=0, color='black', linestyle='-', linewidth=0.5)
    ax5.set_title('SET Performance: TQMemory vs Memcached')
    ax5.set_ylabel('Improvement (%)')
    ax5.set_xlabel('Threads')
    ax5.grid(axis='y', linestyle='--', alpha=0.7)
    for i, v in enumerate(set_improvement):
        ax5.annotate(f'{v:.1f}%', (i, v), ha='center', va='bottom' if v > 0 else 'top', fontsize=9)
    
    colors = ['green' if x > 0 else 'red' for x in get_improvement]
    ax6.bar(range(len(threads)), get_improvement, color=colors, tick_label=threads)
    ax6.axhline(y=0, color='black', linestyle='-', linewidth=0.5)
    ax6.set_title('GET Performance: TQMemory vs Memcached')
    ax6.set_ylabel('Improvement (%)')
    ax6.set_xlabel('Threads')
    ax6.grid(axis='y', linestyle='--', alpha=0.7)
    for i, v in enumerate(get_improvement):
        ax6.annotate(f'{v:.1f}%', (i, v), ha='center', va='bottom' if v > 0 else 'top', fontsize=9)

plt.suptitle('TQMemory Performance Improvement over Memcached', fontsize=14)
plt.tight_layout(rect=[0, 0.03, 1, 0.93])
plt.savefig('getset_benchmark_improvement.png', dpi=150, bbox_inches='tight')
print("Saved: getset_benchmark_improvement.png")
EOF
}

# Main benchmark run
echo ""
echo "###########################################################"
echo "# TQMemory vs Memcached Benchmark"
echo "# Testing with varying thread counts"
echo "###########################################################"
echo ""

# Output file
OUTPUT="getset_benchmark.csv"
echo "Threads,Backend,Protocol,Operation,RPS,TimePerReq(ms),MaxMemory(MB),CPU(%)" > $OUTPUT

# Test with different thread counts
# TQMemory threads vs Memcached threads (-t flag)
for THREADS in 1 2 4 8; do
    run_benchmark $THREADS $THREADS $REQUESTS
done

echo ""
echo "---------------------------------------------------"
echo "Benchmark completed. Results saved to $OUTPUT"
echo "---------------------------------------------------"
column -s, -t $OUTPUT

# Generate visualization
echo ""
echo "Generating visualization..."
generate_visualization

echo ""
echo "============================================="
echo "All benchmarks completed!"
echo "Generated files:"
echo "  - getset_benchmark.csv"
echo "  - getset_benchmark.png"
echo "  - getset_benchmark_improvement.png"
echo "============================================="
echo "Done!"