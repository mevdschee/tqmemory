# TQMemory Performance Optimizations

This document describes the performance optimizations applied to TQMemory to achieve competitive performance with Memcached.

## Summary

| Metric | Before | After | Improvement |
|--------|--------|-------|-------------|
| GET (single client) | 45,358 RPS | 53,125 RPS | **+17%** |
| GET (10 clients) | 326,000 RPS | 365,000 RPS | **+12%** |
| Gap vs Memcached | -16% | -1.6% | **+14 points** |

## Optimization Phases

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

### Phase 3: Direct GET Path with RWMutex

**File**: `pkg/tqmemory/worker.go`, `pkg/tqmemory/sharded.go`

**Change**: Added `DirectGet()` method that bypasses the channel for read operations, using `RWMutex` for concurrent access.

```go
func (w *Worker) DirectGet(key string) ([]byte, uint64, error) {
    w.mu.RLock()
    entry, ok := w.index.Get(key)
    // ... check expiry ...
    w.mu.RUnlock()
    return entry.Value, entry.Cas, nil
}
```

**Result**: +10% improvement. Eliminated channel overhead for GET operations.

---

### Phase 4: sync.Map for Lock-Free Reads

**File**: `pkg/tqmemory/index.go`

**Change**: Replaced the plain map with `sync.Map` to enable lock-free concurrent reads.

```go
type Index struct {
    data sync.Map  // key → *IndexEntry, thread-safe
}

func (idx *Index) Get(key string) (*IndexEntry, bool) {
    val, ok := idx.data.Load(key)
    if !ok {
        return nil, false
    }
    return val.(*IndexEntry), true
}
```

**Result**: Maintained thread safety while enabling concurrent reads without RWMutex overhead.

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

### Phase 7: Batched LRU Touch

**File**: `pkg/tqmemory/worker.go`

**Change**: LRU touches are queued to a buffered channel and processed in batches every 100ms.

```go
// In DirectGet:
select {
case w.touchChan <- entry.Key:
default:
    // Channel full, skip this touch
}

// In worker run loop:
case <-expiryTicker.C:
    w.mu.Lock()
    w.drainTouchChan()  // Process all pending LRU touches
    w.expireKeys()
    w.mu.Unlock()
```

**Result**: +3% improvement. Reduced lock contention by batching LRU updates.

---

## Profiling Results

### pprof CPU Profile (Before Optimizations)

| Function | CPU % |
|----------|-------|
| `runtime.memmove` (value copy) | 15% |
| `runtime.mallocgc` | 46% |
| `sync/atomic.Add` (RWMutex) | 25% |

### pprof CPU Profile (After Optimizations)

| Function | CPU % |
|----------|-------|
| `runtime.procyield` (sync.Map) | 26% |
| `runtime.mapaccess2` (sync.Map) | 17% |
| `workerFor` (inline FNV) | 9% |
| `sync.Map.Load` | 36% (cumulative) |

The remaining overhead is primarily from sync.Map's internal locking mechanism, which is necessary for thread safety.

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

## Future Optimization Opportunities

1. **Custom concurrent map**: Implement a purpose-built concurrent map with less overhead than sync.Map
2. **io_uring**: Use Linux io_uring for more efficient network I/O
3. **Memory pooling**: Pool frequently allocated objects (requests, responses)
4. **NUMA awareness**: Bind workers to specific CPU cores for better cache locality
