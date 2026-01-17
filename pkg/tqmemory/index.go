package tqmemory

import (
	"container/heap"
	"container/list"
	"errors"
	"sync"
)

// Common errors
var (
	ErrKeyNotFound   = errors.New("key not found")
	ErrKeyTooLarge   = errors.New("key too large")
	ErrValueTooLarge = errors.New("value too large")
	ErrKeyExists     = errors.New("key already exists")
	ErrCasMismatch   = errors.New("cas mismatch")
)

// IndexEntry represents an entry in the index
type IndexEntry struct {
	Key    string
	Value  []byte // Value stored directly in the entry
	Expiry int64  // Unix timestamp in milliseconds, 0 = no expiry
	Cas    uint64
}

// ExpiryEntry represents an entry in the expiry heap
type ExpiryEntry struct {
	Expiry int64  // Unix timestamp in milliseconds
	Key    string // Key for lookup
	index  int    // heap index for updates
}

// ExpiryHeap is a min-heap ordered by expiry time
type ExpiryHeap struct {
	entries  []*ExpiryEntry
	keyIndex map[string]int // key → heap index for O(log n) removal
}

func NewExpiryHeap() *ExpiryHeap {
	return &ExpiryHeap{
		entries:  make([]*ExpiryEntry, 0),
		keyIndex: make(map[string]int),
	}
}

func (h *ExpiryHeap) Len() int { return len(h.entries) }

func (h *ExpiryHeap) Less(i, j int) bool {
	return h.entries[i].Expiry < h.entries[j].Expiry
}

func (h *ExpiryHeap) Swap(i, j int) {
	h.entries[i], h.entries[j] = h.entries[j], h.entries[i]
	h.entries[i].index = i
	h.entries[j].index = j
	h.keyIndex[h.entries[i].Key] = i
	h.keyIndex[h.entries[j].Key] = j
}

func (h *ExpiryHeap) Push(x interface{}) {
	entry := x.(*ExpiryEntry)
	entry.index = len(h.entries)
	h.entries = append(h.entries, entry)
	h.keyIndex[entry.Key] = entry.index
}

func (h *ExpiryHeap) Pop() interface{} {
	n := len(h.entries)
	entry := h.entries[n-1]
	h.entries = h.entries[:n-1]
	delete(h.keyIndex, entry.Key)
	return entry
}

// PeekMin returns the entry with the smallest expiry without removing it
func (h *ExpiryHeap) PeekMin() *ExpiryEntry {
	if len(h.entries) == 0 {
		return nil
	}
	return h.entries[0]
}

// Insert adds or updates an entry
func (h *ExpiryHeap) Insert(key string, expiry int64) {
	if idx, ok := h.keyIndex[key]; ok {
		// Update existing
		h.entries[idx].Expiry = expiry
		heap.Fix(h, idx)
	} else {
		// Insert new
		heap.Push(h, &ExpiryEntry{Expiry: expiry, Key: key})
	}
}

// Remove removes an entry by key
func (h *ExpiryHeap) Remove(key string) {
	if idx, ok := h.keyIndex[key]; ok {
		heap.Remove(h, idx)
	}
}

// Index holds all in-memory data structures.
// Uses sync.Map for thread-safe concurrent read/write access.
type Index struct {
	data       sync.Map // key → *IndexEntry, thread-safe
	count      int      // Manual count
	expiryHeap *ExpiryHeap
	lruList    *list.List               // Doubly linked list for LRU ordering
	lruMap     map[string]*list.Element // key → list element for O(1) access
}

func NewIndex() *Index {
	return &Index{
		expiryHeap: NewExpiryHeap(),
		lruList:    list.New(),
		lruMap:     make(map[string]*list.Element),
	}
}

// Get retrieves an entry by key - lock-free with sync.Map
func (idx *Index) Get(key string) (*IndexEntry, bool) {
	val, ok := idx.data.Load(key)
	if !ok {
		return nil, false
	}
	return val.(*IndexEntry), true
}

// Set inserts or updates an entry
func (idx *Index) Set(entry *IndexEntry) {
	_, existed := idx.data.Load(entry.Key)
	idx.data.Store(entry.Key, entry)
	if !existed {
		idx.count++
	}

	// Update expiry heap
	if entry.Expiry > 0 {
		idx.expiryHeap.Insert(entry.Key, entry.Expiry)
	} else {
		idx.expiryHeap.Remove(entry.Key)
	}

	// Update LRU list - move to back (most recently used)
	if elem, ok := idx.lruMap[entry.Key]; ok {
		idx.lruList.MoveToBack(elem)
	} else {
		elem := idx.lruList.PushBack(entry.Key)
		idx.lruMap[entry.Key] = elem
	}
}

// Delete removes an entry by key
func (idx *Index) Delete(key string) *IndexEntry {
	val, loaded := idx.data.LoadAndDelete(key)
	if !loaded {
		return nil
	}
	idx.count--
	entry := val.(*IndexEntry)
	idx.expiryHeap.Remove(key)

	// Remove from LRU list
	if elem, ok := idx.lruMap[key]; ok {
		idx.lruList.Remove(elem)
		delete(idx.lruMap, key)
	}

	return entry
}

// Touch moves a key to the end of the LRU list (most recently used)
func (idx *Index) Touch(key string) {
	if elem, ok := idx.lruMap[key]; ok {
		idx.lruList.MoveToBack(elem)
	}
}

// GetOldest returns the least recently used entry (for eviction)
func (idx *Index) GetOldest() *IndexEntry {
	elem := idx.lruList.Front()
	if elem == nil {
		return nil
	}
	key := elem.Value.(string)
	entry, ok := idx.Get(key)
	if !ok {
		return nil
	}
	return entry
}

// Count returns the number of entries
func (idx *Index) Count() int {
	return idx.count
}

// ExpiryHeapRef returns the expiry heap for background expiration
func (idx *Index) ExpiryHeap() *ExpiryHeap {
	return idx.expiryHeap
}
