# TQMemory

TQMemory is a high-performance, non-persistent memory cache server. It provides a
Memcached-compatible interface, making it ideal as a drop-in replacement for
`memcached`.

Blog post: https://www.tqdev.com/2026-tqmemory-memcache-redis-alternative

## Features

- **Memory Cache**: Ideal as a drop-in replacement for `memcached`
- **Faster than Memcached**: More than 50% faster than Memcached in typical cases
- **Memcached Compatible**: Supports all Memcached commands, text and binary
- **Same CLI Flags**: Uses identical command-line options as memcached

## Requirements

- Go 1.21 or later

## Installation

```bash
go install github.com/mevdschee/tqmemory/cmd/tqmemory@latest
```

Or build from source:

```bash
git clone https://github.com/mevdschee/tqmemory.git
cd tqmemory
go build -o tqmemory ./cmd/tqmemory
```

## Usage

```bash
tqmemory [options]
```

### Command-Line Flags

Uses the same flags as memcached (with long name alternatives):

| Short  | Long           | Default      | Description                              |
| ------ | -------------- | ------------ | ---------------------------------------- |
| `-p`   | `-port`        | `11211`      | TCP port to listen on                    |
| `-l`   | `-listen`      | (all)        | Interface to listen on                   |
| `-m`   | `-memory`      | `64`         | Max memory in megabytes                  |
| `-c`   | `-connections` | `1024`       | Max simultaneous connections             |
| `-t`   | `-threads`     | `4`          | Number of threads                        |
|        | `-config`      |              | Path to [config file](cmd/tqmemory/tqmemory.conf) |

**Fixed limits:** Max key size is 250 bytes. Max value size is 1MB.

### Examples

```bash
# Start with defaults (same as memcached defaults)
tqmemory

# Listen on port 11212 with 128MB memory and 8 threads
tqmemory -p 11212 -m 128 -t 8

# Same using long names
tqmemory -port 11212 -memory 128 -threads 8

# Listen only on localhost
tqmemory -l 127.0.0.1

# Use a config file
tqmemory -config /etc/tqmemory.conf
```

## PHP Configuration

Configure PHP to use TQMemory as a session handler:

```ini
session.save_handler = memcached
session.save_path = "localhost:11211"
```

## Performance

**TQMemory vs Memcached**

Benchmarks were run on a local development environment (Linux, Loopback).
100,000 keys were used for the benchmark, each with a size of 10KB.

### Benchmark Results

![Performance Benchmark](benchmarks/getset/getset_benchmark.png)

### Performance Highlights

- **Write (SET)**: Faster than Memcached in typical workloads
- **Read (GET)**: Faster than Memcached in typical workloads
- **Memory**: Similar memory usage to Memcached

## Testing

```bash
go test ./pkg/tqmemory/...
```

## Architecture

TQMemory stores data in memory using a lock-free, worker-based architecture.
Each worker handles a subset of keys, eliminating lock contention.

See [PROJECT_BRIEF.md](PROJECT_BRIEF.md) for detailed architecture.
