package tqmemory

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

// respChanPool pools response channels to reduce allocation overhead
var respChanPool = sync.Pool{
	New: func() any { return make(chan *Response, 1) },
}

// ShardedCache wraps multiple Worker instances for concurrent access.
// Keys are distributed across workers using FNV-1a hash.
// Each worker is operated by a dedicated goroutine, eliminating lock contention.
type ShardedCache struct {
	workers   []*Worker
	config    Config
	StartTime time.Time
}

// NewSharded creates a new sharded cache with the specified number of workers.
// Each worker handles a subset of keys determined by FNV-1a hash.
func NewSharded(cfg Config, workerCount int) (*ShardedCache, error) {
	if workerCount <= 0 {
		workerCount = DefaultThreadCount
	}

	// Set GOMAXPROCS for optimal parallelism: max(min(cpucount, workers), 1)
	gomaxprocs := runtime.NumCPU()
	if gomaxprocs > workerCount {
		gomaxprocs = workerCount
	}
	if gomaxprocs < 1 {
		gomaxprocs = 1
	}
	runtime.GOMAXPROCS(gomaxprocs)

	sc := &ShardedCache{
		workers:   make([]*Worker, workerCount),
		config:    cfg,
		StartTime: time.Now(),
	}

	// Divide max memory evenly among workers
	maxMemoryPerWorker := cfg.MaxMemory / int64(workerCount)

	// Create a worker for each shard
	for i := 0; i < workerCount; i++ {
		worker := NewWorker(cfg.DefaultTTL, cfg.ChannelCapacity, maxMemoryPerWorker)
		worker.Start()
		sc.workers[i] = worker
	}

	return sc, nil
}

// workerFor returns the worker index for the given key using inline FNV-1a hash.
// Inlined to avoid interface allocation from fnv.New32a().
func (sc *ShardedCache) workerFor(key string) int {
	// FNV-1a hash inlined for performance
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

// Close closes all workers.
func (sc *ShardedCache) Close() error {
	var err error
	for _, worker := range sc.workers {
		if e := worker.Close(); e != nil {
			err = e
		}
	}
	return err
}

// sendRequest sends a request to the appropriate worker and waits for response.
func (sc *ShardedCache) sendRequest(workerIdx int, req *Request) *Response {
	respChan := respChanPool.Get().(chan *Response)
	req.RespChan = respChan
	sc.workers[workerIdx].RequestChan() <- req
	resp := <-respChan
	respChanPool.Put(respChan)
	return resp
}

// Get retrieves a value from the cache.
func (sc *ShardedCache) Get(key string) ([]byte, uint64, error) {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:  OpGet,
		Key: key,
	})
	return resp.Value, resp.Cas, resp.Err
}

// Set stores a value in the cache.
func (sc *ShardedCache) Set(key string, value []byte, ttl time.Duration) (uint64, error) {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:    OpSet,
		Key:   key,
		Value: value,
		TTL:   ttl,
	})
	return resp.Cas, resp.Err
}

// Add stores a value only if it doesn't already exist.
func (sc *ShardedCache) Add(key string, value []byte, ttl time.Duration) (uint64, error) {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:    OpAdd,
		Key:   key,
		Value: value,
		TTL:   ttl,
	})
	return resp.Cas, resp.Err
}

// Replace stores a value only if it already exists.
func (sc *ShardedCache) Replace(key string, value []byte, ttl time.Duration) (uint64, error) {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:    OpReplace,
		Key:   key,
		Value: value,
		TTL:   ttl,
	})
	return resp.Cas, resp.Err
}

// Cas stores a value only if CAS matches.
func (sc *ShardedCache) Cas(key string, value []byte, ttl time.Duration, cas uint64) (uint64, error) {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:    OpCas,
		Key:   key,
		Value: value,
		TTL:   ttl,
		Cas:   cas,
	})
	return resp.Cas, resp.Err
}

// Delete removes a key from the cache.
func (sc *ShardedCache) Delete(key string) error {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:  OpDelete,
		Key: key,
	})
	return resp.Err
}

// Touch updates the TTL of an existing item.
func (sc *ShardedCache) Touch(key string, ttl time.Duration) (uint64, error) {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:  OpTouch,
		Key: key,
		TTL: ttl,
	})
	return resp.Cas, resp.Err
}

// Increment increments a numeric value.
func (sc *ShardedCache) Increment(key string, delta uint64) (uint64, uint64, error) {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:    OpIncr,
		Key:   key,
		Delta: delta,
	})
	// Parse value as uint64
	var val uint64
	for _, b := range resp.Value {
		if b >= '0' && b <= '9' {
			val = val*10 + uint64(b-'0')
		}
	}
	return val, resp.Cas, resp.Err
}

// Decrement decrements a numeric value.
func (sc *ShardedCache) Decrement(key string, delta uint64) (uint64, uint64, error) {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:    OpDecr,
		Key:   key,
		Delta: delta,
	})
	// Parse value as uint64
	var val uint64
	for _, b := range resp.Value {
		if b >= '0' && b <= '9' {
			val = val*10 + uint64(b-'0')
		}
	}
	return val, resp.Cas, resp.Err
}

// Append appends data to an existing value.
func (sc *ShardedCache) Append(key string, value []byte) (uint64, error) {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:    OpAppend,
		Key:   key,
		Value: value,
	})
	return resp.Cas, resp.Err
}

// Prepend prepends data to an existing value.
func (sc *ShardedCache) Prepend(key string, value []byte) (uint64, error) {
	resp := sc.sendRequest(sc.workerFor(key), &Request{
		Op:    OpPrepend,
		Key:   key,
		Value: value,
	})
	return resp.Cas, resp.Err
}

// FlushAll invalidates all items.
func (sc *ShardedCache) FlushAll() {
	for i := range sc.workers {
		sc.sendRequest(i, &Request{Op: OpFlushAll})
	}
}

// Stats returns cache statistics.
func (sc *ShardedCache) Stats() map[string]string {
	totalItems := 0

	for _, worker := range sc.workers {
		totalItems += worker.Index().Count()
	}

	stats := make(map[string]string)
	stats["curr_items"] = fmt.Sprintf("%d", totalItems)
	return stats
}

// GetStartTime returns when the cache was started
func (sc *ShardedCache) GetStartTime() time.Time {
	return sc.StartTime
}
