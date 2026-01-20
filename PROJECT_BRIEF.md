# Project Brief: TQMemory

TQMemory is a high-performance, in-memory caching system implemented in Go,
designed as a **drop-in replacement for Memcached**. It uses the same
command-line flags as Memcached for seamless migration.

## Goals

1. **Memcached Replacement**: 100% compatible with Memcached clients (text and
   binary protocols)
2. **Competitive Performance**: Match or exceed Memcached in GET/SET operations
3. **Same CLI Flags**: Use identical command-line options as Memcached
4. **LRU Eviction**: Memory-limited with automatic LRU eviction
5. **Simplicity**: Easy deployment, minimal configuration required

## Command-Line Flags

Uses the same flags as Memcached:

| Flag   | Default      | Description                                    |
| ------ | ------------ | ---------------------------------------------- |
| `-p`   | `11211`      | TCP port to listen on                          |
| `-s`   |              | Unix socket path (overrides -p and -l)         |
| `-l`   | (all)        | Interface to listen on (default: INADDR_ANY)   |
| `-m`   | `64`         | Max memory to use for items in megabytes       |
| `-c`   | `1024`       | Max simultaneous connections                   |
| `-t`   | `4`          | Number of threads/shards to use                |

**Fixed Limits** (matching Memcached):

| Limit            | Value     | Description                          |
| ---------------- | --------- | ------------------------------------ |
| Max key size     | 250 bytes | Maximum key length                   |
| Max value size   | 1 MB      | Maximum value size                   |

## Architecture

### Concurrency Model

Uses a **sharded, worker-based architecture** with optimized read path:

| Component        | Description                                      |
| ---------------- | ------------------------------------------------ |
| **ShardedCache** | Routes keys to workers via inline FNV-1a hash    |
| **Worker**       | Owns shard data, handles all ops via channel     |
| **Index**        | Simple map for O(1) key lookup (no locks needed) |
| **ExpiryHeap**   | Min-heap for efficient TTL management            |
| **LRU List**     | Doubly linked list for eviction ordering         |

**How it works**:

1. Each worker owns a shard of data (divided by key hash)
2. **All operations**: Sent via buffered channel to worker goroutine
3. Single worker goroutine processes all requests for its shard
4. No locks needed - each shard has exclusive single-threaded access
5. GOMAXPROCS = `max(min(cpu_count, workers), 1)` for optimal parallelism

**Benefits**:

- **Lock-free**: No RWMutex, no lock contention, no synchronization overhead
- **Write serialization**: All operations processed by single worker goroutine
- **Per-worker LRU**: Each shard has independent memory limit and eviction
- **Simple model**: Single-threaded per shard is easier to reason about
- **Predictable latency**: Sequential processing, no lock contention

---

### In-Memory Storage

Pure in-memory storage with LRU eviction:

```go
type IndexEntry struct {
    Key    string   // Key string
    Value  []byte   // Value stored directly in entry
    Expiry int64    // Unix timestamp (ms), 0 = no expiry
    Cas    uint64   // CAS token for compare-and-swap
}
```

---

### Memory Management

- **Memory Tracking**: Each worker tracks `usedMemory` in bytes
- **Memory Limit**: Configurable via `-m` flag (divided among workers)
- **LRU Eviction**: When memory limit is exceeded, evicts least recently used items
- **Access Tracking**: GET/SET/TOUCH operations update LRU list (batched for performance)

---

### Connection Limiting

- **Connection Tracking**: Atomic counter tracks active connections
- **Limit Enforcement**: New connections rejected when limit reached
- **Configurable**: Via `-c` flag (default: 1024)

---

## Supported Commands

Full Memcached protocol support (text and binary):

### Storage Commands
- `set` - Store a key/value
- `add` - Store if key doesn't exist
- `replace` - Store if key exists
- `append` - Append to existing value
- `prepend` - Prepend to existing value
- `cas` - Compare-and-swap update

### Retrieval Commands
- `get` - Retrieve one or more keys
- `gets` - Retrieve with CAS token
- `gat` - Get and touch (update TTL)
- `gats` - Get and touch with CAS

### Other Commands
- `delete` - Remove a key
- `incr/decr` - Increment/decrement numeric value
- `touch` - Update TTL without retrieving
- `flush_all` - Invalidate all items
- `stats` - Server statistics
- `version` - Server version
- `quit` - Close connection

---

## Performance (4 threads)

| Operation | TQMemory     | Memcached    | Difference |
| --------- | ------------ | ------------ | ---------- |
| **SET**   | 156,029 RPS  | 129,919 RPS  | **+20%**   |
| **GET**   | 281,623 RPS  | 281,072 RPS  | **+0.2%**  |

See [OPTIMIZATIONS.md](OPTIMIZATIONS.md) for performance optimization details.

---

## Success Criteria

1. **Performance**: Match or beat Memcached in GET and SET benchmarks
2. **Compatibility**: Pass Memcached protocol compliance tests
3. **Memory Limiting**: Respect `-m` flag with LRU eviction
4. **Connection Limiting**: Respect `-c` flag, reject excess connections
5. **Simplicity**: Drop-in replacement with same CLI flags as Memcached

---

## Non-Goals

- **No Disk Persistence**: TQMemory is purely in-memory
- **No Clustering**: Single-node only (use client-side sharding for distribution)
