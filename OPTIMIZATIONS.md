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
