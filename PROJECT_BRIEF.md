# Project Brief: TQMemory

TQMemory is a high-performance, in-memory caching system implemented in Go,
designed as a **drop-in replacement for memcached**. It uses the same
command-line flags as memcached for seamless migration.

## Goals

1. **Memcached Replacement**: 100% compatible with memcached clients (text and
   binary protocols)
2. **Better Performance**: Outperform memcached in both GET and SET operations
3. **Same CLI Flags**: Use identical command-line options as memcached
4. **LRU Eviction**: Memory-limited with automatic LRU eviction
5. **Simplicity**: Easy deployment, minimal configuration required

## Command-Line Flags

Uses the same flags as memcached:

| Flag   | Default      | Description                                    |
| ------ | ------------ | ---------------------------------------------- |
| `-p`   | `11211`      | TCP port to listen on                          |
| `-l`   | (all)        | Interface to listen on (default: INADDR_ANY)   |
| `-m`   | `64`         | Max memory to use for items in megabytes       |
| `-c`   | `1024`       | Max simultaneous connections                   |
| `-t`   | `4`          | Number of threads to use                       |

**Fixed Limits** (matching memcached):

| Limit            | Value     | Description                          |
| ---------------- | --------- | ------------------------------------ |
| Max key size     | 250 bytes | Maximum key length                   |
| Max value size   | 1 MB      | Maximum value size                   |

## Architecture

### Concurrency Model

Uses a **lock-free, worker-based architecture** with one goroutine per worker:

| Component        | Description                                      |
| ---------------- | ------------------------------------------------ |
| **ShardedCache** | Routes keys to workers via FNV-1a hash           |
| **Worker**       | Single goroutine processing requests via channel |
| **Index**        | B-tree for O(log n) key lookup                   |
| **ExpiryHeap**   | Min-heap for efficient TTL management            |
| **LRU List**     | Doubly linked list for eviction ordering         |

**How it works**:

1. Each worker has a dedicated goroutine that owns all its data
2. Requests are sent via buffered channels (1000 capacity by default)
3. Worker processes requests **sequentially** - no locks needed
4. GOMAXPROCS = `max(min(cpu_count, workers), 1)` for optimal parallelism

**Benefits**:

- **Lock-free**: No mutexes, no lock contention within workers
- **Predictable latency**: Sequential processing, no lock waiting
- **Simple reasoning**: Each worker is single-threaded, no race conditions

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
- **Access Tracking**: Every GET/SET/TOUCH moves item to end of LRU list

---

### Connection Limiting

- **Connection Tracking**: Atomic counter tracks active connections
- **Limit Enforcement**: New connections rejected when limit reached
- **Configurable**: Via `-c` flag (default: 1024)

---

## Supported Commands

Full memcached protocol support (text and binary):

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

## Success Criteria

1. **Performance**: Beat memcached in GET and SET benchmarks
2. **Compatibility**: Pass memcached protocol compliance tests
3. **Memory Limiting**: Respect `-m` flag with LRU eviction
4. **Connection Limiting**: Respect `-c` flag, reject excess connections
5. **Simplicity**: Drop-in replacement with same CLI flags as memcached

---

## Non-Goals

- **No Disk Persistence**: TQMemory is purely in-memory
- **No Clustering**: Single-node only (use client-side sharding for distribution)
