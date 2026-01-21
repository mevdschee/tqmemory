package tqmemory

import (
	"fmt"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/maypok86/otter/v2"
)

// CacheEntry stores a cache value with metadata
type CacheEntry struct {
	Value  []byte
	Cas    uint64
	Expiry int64 // Unix timestamp in milliseconds, 0 = no expiry
}

// ShardedCache wraps an Otter cache for concurrent access.
// Despite the name (kept for API compatibility), this is now a single
// thread-safe cache - sharding is no longer needed with Otter.
type ShardedCache struct {
	cache      *otter.Cache[string, *CacheEntry]
	config     Config
	StartTime  time.Time
	casCounter atomic.Uint64
	DefaultTTL time.Duration
}

// NewSharded creates a new cache with the specified configuration.
// The workerCount parameter now controls GOMAXPROCS for CPU usage.
func NewSharded(cfg Config, workerCount int) (*ShardedCache, error) {
	if workerCount <= 0 {
		workerCount = DefaultThreadCount
	}

	// Set GOMAXPROCS for CPU control
	gomaxprocs := workerCount
	if gomaxprocs > runtime.NumCPU() {
		gomaxprocs = runtime.NumCPU()
	}
	if gomaxprocs < 1 {
		gomaxprocs = 1
	}
	runtime.GOMAXPROCS(gomaxprocs)

	// Create Otter cache with weighted eviction
	cache, err := otter.New(&otter.Options[string, *CacheEntry]{
		MaximumWeight: uint64(cfg.MaxMemory),
		Weigher: func(key string, entry *CacheEntry) uint32 {
			return uint32(len(key) + len(entry.Value))
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create otter cache: %w", err)
	}

	sc := &ShardedCache{
		cache:      cache,
		config:     cfg,
		StartTime:  time.Now(),
		DefaultTTL: cfg.DefaultTTL,
	}
	sc.casCounter.Store(uint64(time.Now().UnixNano()))

	return sc, nil
}

// Close closes the cache.
func (sc *ShardedCache) Close() error {
	sc.cache.InvalidateAll()
	return nil
}

// nextCas generates the next CAS token
func (sc *ShardedCache) nextCas() uint64 {
	return sc.casCounter.Add(1)
}

// isExpired checks if an entry is expired
func isExpired(entry *CacheEntry) bool {
	return entry.Expiry > 0 && entry.Expiry <= time.Now().UnixMilli()
}

// Get retrieves a value from the cache.
func (sc *ShardedCache) Get(key string) ([]byte, uint64, error) {
	entry, ok := sc.cache.GetIfPresent(key)
	if !ok {
		return nil, 0, ErrKeyNotFound
	}

	// Check expiry
	if isExpired(entry) {
		sc.cache.Invalidate(key)
		return nil, 0, ErrKeyNotFound
	}

	return entry.Value, entry.Cas, nil
}

// Set stores a value in the cache.
func (sc *ShardedCache) Set(key string, value []byte, ttl time.Duration) (uint64, error) {
	// Apply default TTL if none specified
	if ttl == 0 && sc.DefaultTTL > 0 {
		ttl = sc.DefaultTTL
	}

	// Calculate expiry
	var expiry int64
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UnixMilli()
	}

	// Generate new CAS
	cas := sc.nextCas()

	// Make a copy of the value
	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)

	// Store in cache
	entry := &CacheEntry{
		Value:  valueCopy,
		Cas:    cas,
		Expiry: expiry,
	}
	sc.cache.Set(key, entry)

	// Set TTL if specified
	if ttl > 0 {
		sc.cache.SetExpiresAfter(key, ttl)
	}

	return cas, nil
}

// Add stores a value only if it doesn't already exist.
// Uses Compute for atomic check-and-set.
func (sc *ShardedCache) Add(key string, value []byte, ttl time.Duration) (uint64, error) {
	// Apply default TTL
	if ttl == 0 && sc.DefaultTTL > 0 {
		ttl = sc.DefaultTTL
	}

	var expiry int64
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UnixMilli()
	}

	var resultCas uint64
	var resultErr error

	// Make value copy outside Compute
	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)

	sc.cache.Compute(key, func(oldEntry *CacheEntry, found bool) (*CacheEntry, otter.ComputeOp) {
		if found && !isExpired(oldEntry) {
			resultErr = ErrKeyExists
			return nil, otter.CancelOp
		}
		resultCas = sc.nextCas()
		return &CacheEntry{
			Value:  valueCopy,
			Cas:    resultCas,
			Expiry: expiry,
		}, otter.WriteOp
	})

	if resultErr != nil {
		return 0, resultErr
	}

	if ttl > 0 {
		sc.cache.SetExpiresAfter(key, ttl)
	}

	return resultCas, nil
}

// Replace stores a value only if it already exists.
// Uses Compute for atomic check-and-set.
func (sc *ShardedCache) Replace(key string, value []byte, ttl time.Duration) (uint64, error) {
	// Apply default TTL
	if ttl == 0 && sc.DefaultTTL > 0 {
		ttl = sc.DefaultTTL
	}

	var expiry int64
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UnixMilli()
	}

	var resultCas uint64
	var resultErr error

	// Make value copy outside Compute
	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)

	sc.cache.Compute(key, func(oldEntry *CacheEntry, found bool) (*CacheEntry, otter.ComputeOp) {
		if !found || isExpired(oldEntry) {
			resultErr = ErrKeyNotFound
			return nil, otter.CancelOp
		}
		resultCas = sc.nextCas()
		return &CacheEntry{
			Value:  valueCopy,
			Cas:    resultCas,
			Expiry: expiry,
		}, otter.WriteOp
	})

	if resultErr != nil {
		return 0, resultErr
	}

	if ttl > 0 {
		sc.cache.SetExpiresAfter(key, ttl)
	}

	return resultCas, nil
}

// Cas stores a value only if CAS matches.
// Uses Compute for atomic compare-and-swap.
func (sc *ShardedCache) Cas(key string, value []byte, ttl time.Duration, cas uint64) (uint64, error) {
	// Apply default TTL
	if ttl == 0 && sc.DefaultTTL > 0 {
		ttl = sc.DefaultTTL
	}

	var expiry int64
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UnixMilli()
	}

	var resultCas uint64
	var resultErr error

	// Make value copy outside Compute
	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)

	sc.cache.Compute(key, func(oldEntry *CacheEntry, found bool) (*CacheEntry, otter.ComputeOp) {
		if !found || isExpired(oldEntry) {
			resultErr = ErrKeyNotFound
			return nil, otter.CancelOp
		}
		if oldEntry.Cas != cas {
			resultErr = ErrCasMismatch
			return nil, otter.CancelOp
		}
		resultCas = sc.nextCas()
		return &CacheEntry{
			Value:  valueCopy,
			Cas:    resultCas,
			Expiry: expiry,
		}, otter.WriteOp
	})

	if resultErr != nil {
		return 0, resultErr
	}

	if ttl > 0 {
		sc.cache.SetExpiresAfter(key, ttl)
	}

	return resultCas, nil
}

// Delete removes a key from the cache.
func (sc *ShardedCache) Delete(key string) error {
	_, deleted := sc.cache.Invalidate(key)
	if !deleted {
		return ErrKeyNotFound
	}
	return nil
}

// Touch updates the TTL of an existing item.
// Uses Compute for atomic update.
func (sc *ShardedCache) Touch(key string, ttl time.Duration) (uint64, error) {
	// Apply default TTL
	if ttl == 0 && sc.DefaultTTL > 0 {
		ttl = sc.DefaultTTL
	}

	var expiry int64
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UnixMilli()
	}

	var resultCas uint64
	var resultErr error

	sc.cache.Compute(key, func(oldEntry *CacheEntry, found bool) (*CacheEntry, otter.ComputeOp) {
		if !found || isExpired(oldEntry) {
			resultErr = ErrKeyNotFound
			return nil, otter.CancelOp
		}
		resultCas = sc.nextCas()
		return &CacheEntry{
			Value:  oldEntry.Value,
			Cas:    resultCas,
			Expiry: expiry,
		}, otter.WriteOp
	})

	if resultErr != nil {
		return 0, resultErr
	}

	if ttl > 0 {
		sc.cache.SetExpiresAfter(key, ttl)
	}

	return resultCas, nil
}

// Increment increments a numeric value.
func (sc *ShardedCache) Increment(key string, delta uint64) (uint64, uint64, error) {
	return sc.doIncrDecr(key, delta, true)
}

// Decrement decrements a numeric value.
func (sc *ShardedCache) Decrement(key string, delta uint64) (uint64, uint64, error) {
	return sc.doIncrDecr(key, delta, false)
}

// Uses Compute for atomic increment/decrement.
func (sc *ShardedCache) doIncrDecr(key string, delta uint64, incr bool) (uint64, uint64, error) {
	var resultVal uint64
	var resultCas uint64
	var resultErr error
	var preserveExpiry int64

	sc.cache.Compute(key, func(oldEntry *CacheEntry, found bool) (*CacheEntry, otter.ComputeOp) {
		if !found || isExpired(oldEntry) {
			resultErr = ErrKeyNotFound
			return nil, otter.CancelOp
		}

		// Parse current value as uint64
		currentStr := string(oldEntry.Value)
		current, err := strconv.ParseUint(currentStr, 10, 64)
		if err != nil {
			resultErr = ErrNotNumeric
			return nil, otter.CancelOp
		}

		// Apply increment/decrement
		if incr {
			resultVal = current + delta
		} else {
			if delta > current {
				resultVal = 0
			} else {
				resultVal = current - delta
			}
		}

		resultCas = sc.nextCas()
		preserveExpiry = oldEntry.Expiry
		return &CacheEntry{
			Value:  []byte(strconv.FormatUint(resultVal, 10)),
			Cas:    resultCas,
			Expiry: preserveExpiry,
		}, otter.WriteOp
	})

	if resultErr != nil {
		return 0, 0, resultErr
	}

	// Preserve TTL if there was one
	if preserveExpiry > 0 {
		remaining := time.Until(time.UnixMilli(preserveExpiry))
		if remaining > 0 {
			sc.cache.SetExpiresAfter(key, remaining)
		}
	}

	return resultVal, resultCas, nil
}

// Append appends data to an existing value.
func (sc *ShardedCache) Append(key string, value []byte) (uint64, error) {
	return sc.doAppendPrepend(key, value, false)
}

// Prepend prepends data to an existing value.
func (sc *ShardedCache) Prepend(key string, value []byte) (uint64, error) {
	return sc.doAppendPrepend(key, value, true)
}

// Uses Compute for atomic append/prepend.
func (sc *ShardedCache) doAppendPrepend(key string, value []byte, prepend bool) (uint64, error) {
	var resultCas uint64
	var resultErr error
	var preserveExpiry int64

	// Make value copy outside Compute
	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)

	sc.cache.Compute(key, func(oldEntry *CacheEntry, found bool) (*CacheEntry, otter.ComputeOp) {
		if !found || isExpired(oldEntry) {
			resultErr = ErrKeyNotFound
			return nil, otter.CancelOp
		}

		// Create new value
		var newValue []byte
		if prepend {
			newValue = make([]byte, len(valueCopy)+len(oldEntry.Value))
			copy(newValue, valueCopy)
			copy(newValue[len(valueCopy):], oldEntry.Value)
		} else {
			newValue = make([]byte, len(oldEntry.Value)+len(valueCopy))
			copy(newValue, oldEntry.Value)
			copy(newValue[len(oldEntry.Value):], valueCopy)
		}

		resultCas = sc.nextCas()
		preserveExpiry = oldEntry.Expiry
		return &CacheEntry{
			Value:  newValue,
			Cas:    resultCas,
			Expiry: preserveExpiry,
		}, otter.WriteOp
	})

	if resultErr != nil {
		return 0, resultErr
	}

	// Preserve TTL if there was one
	if preserveExpiry > 0 {
		remaining := time.Until(time.UnixMilli(preserveExpiry))
		if remaining > 0 {
			sc.cache.SetExpiresAfter(key, remaining)
		}
	}

	return resultCas, nil
}

// FlushAll invalidates all items.
func (sc *ShardedCache) FlushAll() {
	sc.cache.InvalidateAll()
}

// Stats returns cache statistics.
func (sc *ShardedCache) Stats() map[string]string {
	stats := make(map[string]string)
	stats["curr_items"] = fmt.Sprintf("%d", sc.cache.EstimatedSize())
	return stats
}

// GetStartTime returns when the cache was started
func (sc *ShardedCache) GetStartTime() time.Time {
	return sc.StartTime
}
