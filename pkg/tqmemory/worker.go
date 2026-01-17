package tqmemory

import (
	"strconv"
	"sync"
	"time"
)

// Operation types
type OpType int

const (
	OpGet OpType = iota
	OpSet
	OpAdd
	OpReplace
	OpDelete
	OpTouch
	OpCas
	OpIncr
	OpDecr
	OpAppend
	OpPrepend
	OpFlushAll
	OpStats
)

// Request represents a cache operation request
type Request struct {
	Op       OpType
	Key      string
	Value    []byte
	TTL      time.Duration
	Cas      uint64
	Delta    uint64
	RespChan chan *Response
}

// Response represents a cache operation response
type Response struct {
	Value []byte
	Cas   uint64
	Err   error
	Stats map[string]string
}

// Worker is the single-threaded cache worker
type Worker struct {
	index      *Index
	reqChan    chan *Request
	stopChan   chan struct{}
	wg         sync.WaitGroup
	casCounter uint64
	DefaultTTL time.Duration
	maxMemory  int64 // Max memory in bytes (0 = unlimited)
	usedMemory int64 // Current memory usage
	evictions  uint64
}

// NewWorker creates a new worker
func NewWorker(defaultTTL time.Duration, channelCapacity int, maxMemory int64) *Worker {
	return &Worker{
		index:      NewIndex(),
		reqChan:    make(chan *Request, channelCapacity),
		stopChan:   make(chan struct{}),
		casCounter: uint64(time.Now().UnixNano()),
		DefaultTTL: defaultTTL,
		maxMemory:  maxMemory,
		usedMemory: 0,
		evictions:  0,
	}
}

// Start starts the worker goroutine
func (w *Worker) Start() {
	w.wg.Add(1)
	go w.run()
}

// Stop stops the worker and waits for it to finish
func (w *Worker) Stop() {
	close(w.stopChan)
	w.wg.Wait()
}

// RequestChan returns the request channel
func (w *Worker) RequestChan() chan *Request {
	return w.reqChan
}

// Index returns the worker's index for stats access
func (w *Worker) Index() *Index {
	return w.index
}

// UsedMemory returns current memory usage in bytes
func (w *Worker) UsedMemory() int64 {
	return w.usedMemory
}

// Evictions returns the number of LRU evictions
func (w *Worker) Evictions() uint64 {
	return w.evictions
}

// Close stops the worker
func (w *Worker) Close() error {
	w.Stop()
	return nil
}

func (w *Worker) run() {
	defer w.wg.Done()

	// Background expiration ticker
	expiryTicker := time.NewTicker(100 * time.Millisecond)
	defer expiryTicker.Stop()

	for {
		select {
		case req := <-w.reqChan:
			w.handleRequest(req)
		case <-expiryTicker.C:
			w.expireKeys()
		case <-w.stopChan:
			return
		}
	}
}

func (w *Worker) expireKeys() {
	now := time.Now().UnixMilli()
	for {
		entry := w.index.expiryHeap.PeekMin()
		if entry == nil || entry.Expiry > now {
			break
		}
		// Remove expired key and update memory
		deleted := w.index.Delete(entry.Key)
		if deleted != nil {
			w.usedMemory -= int64(len(deleted.Key) + len(deleted.Value))
		}
	}
}

// evictLRU evicts the least recently used items until we're under maxMemory
func (w *Worker) evictLRU(needed int64) {
	if w.maxMemory == 0 {
		return // No limit
	}

	// Evict items from expiry heap (oldest first) until we have enough space
	// This is a simple approximation of LRU using expiry time
	for w.usedMemory+needed > w.maxMemory {
		// Get the oldest item (lowest expiry or oldest insertion for non-expiring items)
		oldest := w.index.GetOldest()
		if oldest == nil {
			break // No more items to evict
		}

		w.usedMemory -= int64(len(oldest.Key) + len(oldest.Value))
		w.index.Delete(oldest.Key)
		w.evictions++
	}
}

func (w *Worker) handleRequest(req *Request) {
	var resp *Response

	switch req.Op {
	case OpGet:
		resp = w.handleGet(req)
	case OpSet:
		resp = w.handleSet(req)
	case OpAdd:
		resp = w.handleAdd(req)
	case OpReplace:
		resp = w.handleReplace(req)
	case OpCas:
		resp = w.handleCas(req)
	case OpDelete:
		resp = w.handleDelete(req)
	case OpTouch:
		resp = w.handleTouch(req)
	case OpIncr:
		resp = w.handleIncr(req)
	case OpDecr:
		resp = w.handleDecr(req)
	case OpAppend:
		resp = w.handleAppend(req)
	case OpPrepend:
		resp = w.handlePrepend(req)
	case OpFlushAll:
		resp = w.handleFlushAll()
	case OpStats:
		resp = w.handleStats()
	default:
		resp = &Response{Err: ErrKeyNotFound}
	}

	req.RespChan <- resp
}

func (w *Worker) handleGet(req *Request) *Response {
	entry, ok := w.index.Get(req.Key)
	if !ok {
		return &Response{Err: ErrKeyNotFound}
	}

	// Check expiry
	if entry.Expiry > 0 && entry.Expiry <= time.Now().UnixMilli() {
		w.usedMemory -= int64(len(entry.Key) + len(entry.Value))
		w.index.Delete(req.Key)
		return &Response{Err: ErrKeyNotFound}
	}

	// Update access time for LRU
	w.index.Touch(entry.Key)

	return &Response{Value: entry.Value, Cas: entry.Cas}
}

func (w *Worker) handleSet(req *Request) *Response {
	return w.doSet(req.Key, req.Value, req.TTL, 0, false)
}

func (w *Worker) handleAdd(req *Request) *Response {
	entry, ok := w.index.Get(req.Key)
	if ok && (entry.Expiry == 0 || entry.Expiry > time.Now().UnixMilli()) {
		return &Response{Err: ErrKeyExists}
	}
	return w.doSet(req.Key, req.Value, req.TTL, 0, false)
}

func (w *Worker) handleReplace(req *Request) *Response {
	entry, ok := w.index.Get(req.Key)
	if !ok || (entry.Expiry > 0 && entry.Expiry <= time.Now().UnixMilli()) {
		return &Response{Err: ErrKeyNotFound}
	}
	return w.doSet(req.Key, req.Value, req.TTL, 0, false)
}

func (w *Worker) handleCas(req *Request) *Response {
	entry, ok := w.index.Get(req.Key)
	if !ok || (entry.Expiry > 0 && entry.Expiry <= time.Now().UnixMilli()) {
		return &Response{Err: ErrKeyNotFound}
	}
	if entry.Cas != req.Cas {
		return &Response{Err: ErrCasMismatch}
	}
	return w.doSet(req.Key, req.Value, req.TTL, req.Cas, true)
}

func (w *Worker) doSet(key string, value []byte, ttl time.Duration, existingCas uint64, checkCas bool) *Response {
	// Apply default TTL if none specified
	if ttl == 0 && w.DefaultTTL > 0 {
		ttl = w.DefaultTTL
	}

	// Calculate expiry
	var expiry int64
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UnixMilli()
	}

	// Calculate memory needed for this entry
	entrySize := int64(len(key) + len(value))

	// Check if key already exists and get its current size
	var oldSize int64
	if existing, ok := w.index.Get(key); ok {
		oldSize = int64(len(existing.Key) + len(existing.Value))
	}

	// Evict if needed (only for new memory, not replacements)
	additionalMemory := entrySize - oldSize
	if additionalMemory > 0 && w.maxMemory > 0 {
		w.evictLRU(additionalMemory)
	}

	// Generate new CAS
	w.casCounter++
	cas := w.casCounter

	// Make a copy of the value
	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)

	// Store in index
	entry := &IndexEntry{
		Key:    key,
		Value:  valueCopy,
		Expiry: expiry,
		Cas:    cas,
	}
	w.index.Set(entry)

	// Update memory tracking
	w.usedMemory += additionalMemory

	return &Response{Cas: cas}
}

func (w *Worker) handleDelete(req *Request) *Response {
	entry := w.index.Delete(req.Key)
	if entry == nil {
		return &Response{Err: ErrKeyNotFound}
	}
	w.usedMemory -= int64(len(entry.Key) + len(entry.Value))
	return &Response{}
}

func (w *Worker) handleTouch(req *Request) *Response {
	entry, ok := w.index.Get(req.Key)
	if !ok || (entry.Expiry > 0 && entry.Expiry <= time.Now().UnixMilli()) {
		return &Response{Err: ErrKeyNotFound}
	}

	// Apply new TTL
	ttl := req.TTL
	if ttl == 0 && w.DefaultTTL > 0 {
		ttl = w.DefaultTTL
	}

	var expiry int64
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UnixMilli()
	}

	// Update CAS
	w.casCounter++
	entry.Cas = w.casCounter
	entry.Expiry = expiry
	w.index.Set(entry)
	w.index.Touch(entry.Key)

	return &Response{Cas: entry.Cas}
}

func (w *Worker) handleIncr(req *Request) *Response {
	return w.doIncrDecr(req.Key, req.Delta, true)
}

func (w *Worker) handleDecr(req *Request) *Response {
	return w.doIncrDecr(req.Key, req.Delta, false)
}

func (w *Worker) doIncrDecr(key string, delta uint64, incr bool) *Response {
	entry, ok := w.index.Get(key)
	if !ok || (entry.Expiry > 0 && entry.Expiry <= time.Now().UnixMilli()) {
		return &Response{Err: ErrKeyNotFound}
	}

	// Parse current value as uint64
	currentStr := string(entry.Value)
	current, err := strconv.ParseUint(currentStr, 10, 64)
	if err != nil {
		current = 0
	}

	// Apply increment/decrement
	var newVal uint64
	if incr {
		newVal = current + delta
	} else {
		if delta > current {
			newVal = 0
		} else {
			newVal = current - delta
		}
	}

	// Calculate memory change
	oldSize := int64(len(entry.Value))
	newValStr := strconv.FormatUint(newVal, 10)
	newSize := int64(len(newValStr))

	w.casCounter++
	entry.Value = []byte(newValStr)
	entry.Cas = w.casCounter
	w.index.Set(entry)
	w.index.Touch(entry.Key)

	w.usedMemory += (newSize - oldSize)

	return &Response{Value: []byte(newValStr), Cas: entry.Cas}
}

func (w *Worker) handleAppend(req *Request) *Response {
	return w.doAppendPrepend(req.Key, req.Value, false)
}

func (w *Worker) handlePrepend(req *Request) *Response {
	return w.doAppendPrepend(req.Key, req.Value, true)
}

func (w *Worker) doAppendPrepend(key string, value []byte, prepend bool) *Response {
	entry, ok := w.index.Get(key)
	if !ok || (entry.Expiry > 0 && entry.Expiry <= time.Now().UnixMilli()) {
		return &Response{Err: ErrKeyNotFound}
	}

	// Calculate new memory needed
	additionalMemory := int64(len(value))

	// Evict if needed
	if w.maxMemory > 0 && additionalMemory > 0 {
		w.evictLRU(additionalMemory)
	}

	// Create new value
	var newValue []byte
	if prepend {
		newValue = make([]byte, len(value)+len(entry.Value))
		copy(newValue, value)
		copy(newValue[len(value):], entry.Value)
	} else {
		newValue = make([]byte, len(entry.Value)+len(value))
		copy(newValue, entry.Value)
		copy(newValue[len(entry.Value):], value)
	}

	// Update entry
	w.casCounter++
	entry.Value = newValue
	entry.Cas = w.casCounter
	w.index.Set(entry)
	w.index.Touch(entry.Key)

	w.usedMemory += additionalMemory

	return &Response{Cas: entry.Cas}
}

func (w *Worker) handleFlushAll() *Response {
	// Create new empty index
	w.index = NewIndex()
	w.usedMemory = 0
	return &Response{}
}

func (w *Worker) handleStats() *Response {
	stats := make(map[string]string)
	stats["curr_items"] = strconv.Itoa(w.index.Count())
	stats["bytes"] = strconv.FormatInt(w.usedMemory, 10)
	stats["evictions"] = strconv.FormatUint(w.evictions, 10)
	return &Response{Stats: stats}
}
