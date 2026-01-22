# TQMemory Performance Optimizations

This document describes the performance optimization phases applied to TQMemory to achieve competitive performance with Memcached.

TQMemory is optimized for write-heavy workloads with larger values (typical SQL query result caching). It is tested with 4-8 threads, 10 clients, 10KB values, Unix sockets.

### Phase 1: sync.Pool for Response Channels

**File**: `pkg/tqmemory/sharded.go`

**Change**: Added `sync.Pool` to reuse response channels instead of allocating new ones per request.

```go
var respChanPool = sync.Pool{
    New: func() any { return make(chan *Response, 1) },
}
```

**Result**: Minimal impact (~0%) - channel allocation was not the main bottleneck.

---

### Phase 2: Replace B-tree with Map

**File**: `pkg/tqmemory/index.go`

**Change**: Replaced `github.com/google/btree` with native `map[string]*IndexEntry` for O(1) lookups.

**Before**:
```go
type Index struct {
    btree *btree.BTree
}
```

**After**:
```go
type Index struct {
    data map[string]*IndexEntry
}
```

**Result**: +2.3% improvement. Eliminated type assertion overhead and provided constant-time lookups.

---

### Phase 3: Direct GET Path with RWMutex (Reverted)

**File**: `pkg/tqmemory/worker.go`, `pkg/tqmemory/sharded.go`

**Change**: Added `DirectGet()` method that bypasses the channel for read operations, using `RWMutex` for concurrent access.

**Result**: +10% improvement. Eliminated channel overhead for GET operations.

**Status**: **Reverted** in favor of a fully lock-free design. All operations (including GET) now go through the worker channel, eliminating the need for any locks. The single-writer-per-shard model is simpler and avoids lock contention entirely.

---

### Phase 4: Custom Concurrent Map (Replaces sync.Map)

**File**: `pkg/tqmemory/index.go`

**Change**: Replaced sync.Map with regular map + RWMutex, added `lruElem` pointer to IndexEntry, removed `lruMap`.

```go
type IndexEntry struct {
    Key     string
    Value   []byte
    Expiry  int64
    Cas     uint64
    lruElem *list.Element  // Direct pointer to LRU element
}

type Index struct {
    data       map[string]*IndexEntry
    expiryHeap *ExpiryHeap
    lruList    *list.List  // Stores *IndexEntry directly
}
```

**Result**: Better performance than sync.Map for this workload. RWMutex allows concurrent reads while serializing writes through the worker goroutine.

---

### Phase 5: Inline FNV-1a Hash

**File**: `pkg/tqmemory/sharded.go`

**Change**: Inlined the FNV-1a hash function to avoid interface allocation from `fnv.New32a()`.

**Before**:
```go
func (sc *ShardedCache) workerFor(key string) int {
    h := fnv.New32a()
    h.Write([]byte(key))
    return int(h.Sum32()) % len(sc.workers)
}
```

**After**:
```go
func (sc *ShardedCache) workerFor(key string) int {
    const (
        offset32 = uint32(2166136261)
        prime32  = uint32(16777619)
    )
    hash := offset32
    for i := 0; i < len(key); i++ {
        hash ^= uint32(key[i])
        hash *= prime32
    }
    return int(hash) % len(sc.workers)
}
```

**Result**: +4% improvement at cache layer. Eliminated interface allocation overhead.

---

### Phase 6: Remove Value Copy in GET

**File**: `pkg/tqmemory/worker.go`

**Change**: Return the entry value slice directly instead of copying it.

**Before**:
```go
value := make([]byte, len(entry.Value))
copy(value, entry.Value)
return value, cas, nil
```

**After**:
```go
return entry.Value, entry.Cas, nil
```

**Result**: +5% improvement. Eliminated allocation and memcpy for every GET.

**Trade-off**: Caller must not modify the returned slice. This is safe because the server only reads the value to write it to the network.

---

### Phase 7: Batched LRU Touch (Reverted)

**File**: `pkg/tqmemory/worker.go`

**Change**: LRU touches were queued to a buffered channel and processed in batches every 100ms.

**Result**: +3% improvement. Reduced lock contention by batching LRU updates.

**Status**: **Reverted** along with Phase 3. Since all operations now go through the worker channel (single-threaded per shard), LRU touches are processed inline with no lock contention. The batching mechanism is no longer needed.

---

## Shard Tuning

Optimal shard count was determined experimentally:

| Shards | GET (1 client) | GET (10 clients) |
|--------|----------------|------------------|
| 2 | 50K | 238K |
| **4** | **53K** | **365K** |
| 8 | 53K | 252K |
| 16 | 52K | 277K |

**4 shards is optimal** for this workload, balancing:
- Too few shards = more contention per shard
- Too many shards = more goroutine scheduling overhead

---

## Memory Optimization

Key deduplication saves ~68 bytes per entry:
- Removed `lruMap`: ~48 bytes per entry
- Store `*IndexEntry` in LRU list instead of key string: ~20 bytes per key

--- 

## Memory Pooling

Uses `sync.Pool` to reduce allocations in the hot path:

**Pooled buffers:**
- **Extras buffer pool**: 4-byte buffers for GET response flags
- **Small body pool**: Up to 1KB (covers most key-only requests like GET)
- **Medium body pool**: Up to 64KB (covers most SET operations)

Requests larger than 64KB still allocate fresh buffers.

---

## Attempted Optimizations (Not Used)

### CPU Affinity / NUMA Pinning
Tested pinning worker goroutines to specific CPU cores using `sched_setaffinity`. This **reduced performance** because:
- Go's scheduler is designed to balance work efficiently across threads
- Pinning prevents the scheduler from using idle CPUs when I/O waiting
- For cache workloads with mixed read/write, flexibility is more important than locality

### io_uring
Tested using the Gain framework for io_uring networking. Results:
- Small values (100 bytes): ~37K RPS - similar to standard
- Large values (10KB+): Stalls due to buffering issues in Gain framework
- Standard Go networking is already highly optimized for this workload

---

## Network Performance Experiments (January 2026)

Extensive testing to understand network bottlenecks and optimize throughput.

### Baseline Measurements

| Test | GET RPS | Notes |
|------|---------|-------|
| Package mode (no network) | 3.76M | Channel architecture ceiling |
| Network mode (10 clients) | 236K | TCP overhead dominant |
| Network mode (100 clients) | 244K | Similar - syscall bound |

CPU profiling of network mode showed **80% of CPU time in syscalls** (`read`/`write`), confirming network I/O as the bottleneck.

### Alternative Cache Backends

| Backend | Package Mode | Network Mode | Notes |
|---------|--------------|--------------|-------|
| TQMemory (channels) | 3.76M | 236K | Current implementation |
| sync.Map | 298M | - | No LRU, unsafe for production |
| BigCache | 19.1M | 299K | 5x faster package, 27% faster network |

BigCache uses sharded RWMutex instead of channels, reducing lock contention for reads.

### Alternative Network Libraries

| Library | 10 clients | 100 clients | Notes |
|---------|------------|-------------|-------|
| Standard net | 236K | 244K | Goroutine per connection |
| gnet (event-loop) | 76K | 152K | 40% slower than std net |
| gaio (io_uring) | 70K | 130K | Go bindings add overhead |

Go's goroutine-per-connection model outperforms event-loop alternatives for this workload.

### Scatter-Gather I/O (writev)

Tested `net.Buffers` to send header+extras+value in single syscall:
- Result: **233K RPS** (same as baseline 235K)
- No improvement because Go's `bufio.Writer` already coalesces writes

### Maximum Network Ceiling (Dummy Server)

Removed cache entirely to measure pure network overhead:

| Server | GET RPS | Notes |
|--------|---------|-------|
| Go dummy | 300K | No cache access |
| Rust (tokio) | 316K | +5% vs Go |
| TQMemory | 236K | Cache adds ~20% overhead |

**Key finding**: Rust provides only 5% improvement over Go for pure network throughput.

### Network Optimization Conclusions

1. **Network syscalls dominate** (~80% of CPU time)
2. **Go's net package is near-optimal** - event-loop alternatives are slower
3. **Cache overhead is ~20%** (300K ceiling → 236K actual)
4. **Language choice matters little** - Rust only 5% faster than Go
5. **The ~240K RPS ceiling is fundamental** to synchronous TCP request-response

Further gains would require:
- Client-side pipelining (batch requests)
- Protocol changes (UDP, custom framing)
- Kernel bypass (DPDK, AF_XDP)
