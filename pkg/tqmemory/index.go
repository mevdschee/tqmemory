package tqmemory

import (
	"container/heap"
	"container/list"
	"errors"
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
	Key     string
	Value   []byte // Value stored directly in the entry
	Expiry  int64  // Unix timestamp in milliseconds, 0 = no expiry
	Cas     uint64
	lruElem *list.Element // Direct pointer to LRU element (avoids lruMap lookup)
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
// Uses regular map - caller must hold appropriate lock (RLock for Get, Lock for writes).
// LRU list stores *IndexEntry directly, avoiding key duplication.
type Index struct {
	data       map[string]*IndexEntry // key → *IndexEntry
	expiryHeap *ExpiryHeap
	lruList    *list.List // Stores *IndexEntry directly
}

func NewIndex() *Index {
	return &Index{
		data:       make(map[string]*IndexEntry),
		expiryHeap: NewExpiryHeap(),
		lruList:    list.New(),
	}
}

// Get retrieves an entry by key - caller must hold RLock
func (idx *Index) Get(key string) (*IndexEntry, bool) {
	entry, ok := idx.data[key]
	return entry, ok
}

// Set inserts or updates an entry - caller must hold Lock
func (idx *Index) Set(entry *IndexEntry) {
	old, existed := idx.data[entry.Key]
	idx.data[entry.Key] = entry

	// Update expiry heap
	if entry.Expiry > 0 {
		idx.expiryHeap.Insert(entry.Key, entry.Expiry)
	} else {
		idx.expiryHeap.Remove(entry.Key)
	}

	// Update LRU list
	if existed && old.lruElem != nil {
		// Reuse existing element, just update the pointer
		old.lruElem.Value = entry
		entry.lruElem = old.lruElem
		idx.lruList.MoveToBack(entry.lruElem)
	} else {
		// New entry - add to LRU list
		entry.lruElem = idx.lruList.PushBack(entry)
	}
}

// Delete removes an entry by key - caller must hold Lock
func (idx *Index) Delete(key string) *IndexEntry {
	entry, ok := idx.data[key]
	if !ok {
		return nil
	}
	delete(idx.data, key)
	idx.expiryHeap.Remove(key)

	// Remove from LRU list using direct pointer
	if entry.lruElem != nil {
		idx.lruList.Remove(entry.lruElem)
		entry.lruElem = nil
	}

	return entry
}

// Touch moves a key to the end of the LRU list (most recently used)
func (idx *Index) Touch(key string) {
	entry, ok := idx.data[key]
	if ok && entry.lruElem != nil {
		idx.lruList.MoveToBack(entry.lruElem)
	}
}

// GetOldest returns the least recently used entry (for eviction)
func (idx *Index) GetOldest() *IndexEntry {
	elem := idx.lruList.Front()
	if elem == nil {
		return nil
	}
	return elem.Value.(*IndexEntry)
}

// Count returns the number of entries
func (idx *Index) Count() int {
	return len(idx.data)
}

// ExpiryHeapRef returns the expiry heap for background expiration
func (idx *Index) ExpiryHeap() *ExpiryHeap {
	return idx.expiryHeap
}
