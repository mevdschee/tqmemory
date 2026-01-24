package tqmemory

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func setupTestCache(t *testing.T) (*ShardedCache, func()) {
	config := DefaultConfig()

	c, err := NewSharded(config, 4) // Use 4 workers for tests
	if err != nil {
		t.Fatal(err)
	}

	return c, func() {
		c.Close()
	}
}

func TestSetGet(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Set a key
	cas, err := c.Set("key1", []byte("value1"), 0)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if cas == 0 {
		t.Error("Expected non-zero CAS")
	}

	// Get the key
	val, getCas, _, err := c.Get("key1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "value1" {
		t.Errorf("Expected 'value1', got '%s'", val)
	}
	if getCas != cas {
		t.Errorf("CAS mismatch: set=%d, get=%d", cas, getCas)
	}

	// Overwrite
	newCas, err := c.Set("key1", []byte("value2"), 0)
	if err != nil {
		t.Fatalf("Set overwrite failed: %v", err)
	}
	if newCas == cas {
		t.Error("CAS should change on overwrite")
	}

	// Verify new value
	val, _, _, _ = c.Get("key1")
	if string(val) != "value2" {
		t.Errorf("Expected 'value2', got '%s'", val)
	}
}

func TestAdd(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Add to non-existent key should succeed
	cas, err := c.Add("key1", []byte("value1"), 0)
	if err != nil {
		t.Fatalf("Add to new key failed: %v", err)
	}
	if cas == 0 {
		t.Error("Expected non-zero CAS")
	}

	// Verify value
	val, _, _, err := c.Get("key1")
	if err != nil || string(val) != "value1" {
		t.Errorf("Get after Add failed: val=%s, err=%v", val, err)
	}

	// Add to existing key should fail with ErrKeyExists
	_, err = c.Add("key1", []byte("value2"), 0)
	if err != ErrKeyExists {
		t.Errorf("Expected ErrKeyExists for Add on existing key, got %v", err)
	}

	// Verify original value unchanged
	val, _, _, _ = c.Get("key1")
	if string(val) != "value1" {
		t.Errorf("Value changed after failed Add: %s", val)
	}
}

func TestReplace(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Replace on non-existent key should fail with ErrKeyNotFound
	_, err := c.Replace("key1", []byte("value1"), 0)
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound for Replace on missing key, got %v", err)
	}

	// Set a key first
	c.Set("key1", []byte("original"), 0)

	// Replace should succeed
	cas, err := c.Replace("key1", []byte("replaced"), 0)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if cas == 0 {
		t.Error("Expected non-zero CAS")
	}

	// Verify value changed
	val, _, _, _ := c.Get("key1")
	if string(val) != "replaced" {
		t.Errorf("Expected 'replaced', got '%s'", val)
	}
}

func TestCas(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// CAS on non-existent key should fail with ErrKeyNotFound
	_, err := c.Cas("key1", []byte("value"), 0, 12345)
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound for CAS on missing key, got %v", err)
	}

	// Set a key
	originalCas, _ := c.Set("key1", []byte("original"), 0)

	// CAS with wrong token should fail with ErrCasMismatch
	_, err = c.Cas("key1", []byte("wrong"), 0, originalCas+1)
	if err != ErrCasMismatch {
		t.Errorf("Expected ErrCasMismatch for CAS mismatch, got %v", err)
	}

	// Verify value unchanged
	val, _, _, _ := c.Get("key1")
	if string(val) != "original" {
		t.Errorf("Value changed after failed CAS: %s", val)
	}

	// CAS with correct token should succeed
	newCas, err := c.Cas("key1", []byte("updated"), 0, originalCas)
	if err != nil {
		t.Fatalf("CAS with correct token failed: %v", err)
	}
	if newCas == originalCas {
		t.Error("CAS should return new token")
	}

	// Verify value changed
	val, _, _, _ = c.Get("key1")
	if string(val) != "updated" {
		t.Errorf("Expected 'updated', got '%s'", val)
	}
}

func TestCasConcurrency(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	const numGoroutines = 100
	const key = "counter"

	// Initialize counter to 0
	c.Set(key, []byte("0"), 0)

	// Launch goroutines that each increment the counter using CAS
	var wg sync.WaitGroup
	successCount := int64(0)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Each goroutine tries to increment until it succeeds
			for {
				// Get current value and CAS token
				val, cas, _, err := c.Get(key)
				if err != nil {
					continue
				}

				// Parse current value
				current := 0
				fmt.Sscanf(string(val), "%d", &current)

				// Try to increment with CAS
				newVal := fmt.Sprintf("%d", current+1)
				_, err = c.Cas(key, []byte(newVal), 0, cas)
				if err == nil {
					// CAS succeeded, increment success counter
					atomic.AddInt64(&successCount, 1)
					return
				}
				// CAS failed (concurrent modification), retry
			}
		}()
	}

	wg.Wait()

	// Verify final counter value equals number of goroutines
	val, _, _, _ := c.Get(key)
	finalValue := 0
	fmt.Sscanf(string(val), "%d", &finalValue)

	if finalValue != numGoroutines {
		t.Errorf("Expected counter=%d, got %d (CAS race condition!)", numGoroutines, finalValue)
	}

	if successCount != numGoroutines {
		t.Errorf("Expected %d successful CAS operations, got %d", numGoroutines, successCount)
	}

	t.Logf("CAS concurrency test passed: %d goroutines, final counter=%d", numGoroutines, finalValue)
}

func TestDelete(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Delete non-existent key should fail with ErrKeyNotFound
	err := c.Delete("key1")
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound for Delete on missing key, got %v", err)
	}

	// Set a key
	c.Set("key1", []byte("value"), 0)

	// Delete should succeed
	err = c.Delete("key1")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Get should fail with ErrKeyNotFound
	_, _, _, err = c.Get("key1")
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound after Delete, got %v", err)
	}

	// Delete again should fail with ErrKeyNotFound
	err = c.Delete("key1")
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound for second Delete, got %v", err)
	}
}

func TestTouch(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Touch non-existent key should fail with ErrKeyNotFound
	_, err := c.Touch("key1", time.Hour)
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound for Touch on missing key, got %v", err)
	}

	// Set a key with short TTL
	c.Set("key1", []byte("value"), 1*time.Second)

	// Touch to extend TTL
	cas, err := c.Touch("key1", 1*time.Hour)
	if err != nil {
		t.Fatalf("Touch failed: %v", err)
	}
	if cas == 0 {
		t.Error("Expected non-zero CAS")
	}

	// Verify value still accessible
	val, _, _, err := c.Get("key1")
	if err != nil || string(val) != "value" {
		t.Errorf("Get after Touch failed")
	}
}

func TestFlushAll(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Set multiple keys
	c.Set("key1", []byte("value1"), 0)
	c.Set("key2", []byte("value2"), 0)
	c.Set("key3", []byte("value3"), 0)

	// Verify they exist
	_, _, _, err := c.Get("key1")
	if err != nil {
		t.Fatal("Key1 should exist before flush")
	}

	// Flush all
	c.FlushAll()

	// All keys should be gone (ErrKeyNotFound)
	_, _, _, err = c.Get("key1")
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound after FlushAll, got %v", err)
	}
	_, _, _, err = c.Get("key2")
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound after FlushAll, got %v", err)
	}
	_, _, _, err = c.Get("key3")
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound after FlushAll, got %v", err)
	}
}

func TestIncrement(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Increment non-existent key should fail with ErrKeyNotFound
	_, _, err := c.Increment("counter", 1)
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound for Increment on missing key, got %v", err)
	}

	// Set numeric value
	c.Set("counter", []byte("10"), 0)

	// Increment
	newVal, cas, err := c.Increment("counter", 5)
	if err != nil {
		t.Fatalf("Increment failed: %v", err)
	}
	if newVal != 15 {
		t.Errorf("Expected 15, got %d", newVal)
	}
	if cas == 0 {
		t.Error("Expected non-zero CAS")
	}

	// Verify stored value
	val, _, _, _ := c.Get("counter")
	if string(val) != "15" {
		t.Errorf("Expected '15', got '%s'", val)
	}
}

func TestDecrement(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Set numeric value
	c.Set("counter", []byte("10"), 0)

	// Decrement
	newVal, cas, err := c.Decrement("counter", 3)
	if err != nil {
		t.Fatalf("Decrement failed: %v", err)
	}
	if newVal != 7 {
		t.Errorf("Expected 7, got %d", newVal)
	}
	if cas == 0 {
		t.Error("Expected non-zero CAS")
	}

	// Decrement below zero should floor at 0
	newVal, _, err = c.Decrement("counter", 100)
	if err != nil {
		t.Fatalf("Decrement failed: %v", err)
	}
	if newVal != 0 {
		t.Errorf("Expected 0 (floor), got %d", newVal)
	}

	// Verify stored value
	val, _, _, _ := c.Get("counter")
	if string(val) != "0" {
		t.Errorf("Expected '0', got '%s'", val)
	}
}

func TestAppend(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Append to non-existent key should fail with ErrKeyNotFound
	_, err := c.Append("key1", []byte("suffix"))
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound for Append on missing key, got %v", err)
	}

	// Set a key
	c.Set("key1", []byte("hello"), 0)

	// Append
	cas, err := c.Append("key1", []byte(" world"))
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	if cas == 0 {
		t.Error("Expected non-zero CAS")
	}

	// Verify
	val, _, _, _ := c.Get("key1")
	if string(val) != "hello world" {
		t.Errorf("Expected 'hello world', got '%s'", val)
	}
}

func TestPrepend(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Prepend to non-existent key should fail with ErrKeyNotFound
	_, err := c.Prepend("key1", []byte("prefix"))
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound for Prepend on missing key, got %v", err)
	}

	// Set a key
	c.Set("key1", []byte("world"), 0)

	// Prepend
	cas, err := c.Prepend("key1", []byte("hello "))
	if err != nil {
		t.Fatalf("Prepend failed: %v", err)
	}
	if cas == 0 {
		t.Error("Expected non-zero CAS")
	}

	// Verify
	val, _, _, _ := c.Get("key1")
	if string(val) != "hello world" {
		t.Errorf("Expected 'hello world', got '%s'", val)
	}
}

func TestStats(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Empty cache
	stats := c.Stats()
	if stats["curr_items"] != "0" {
		t.Errorf("Expected 0 items, got %s", stats["curr_items"])
	}

	// Add items
	c.Set("key1", []byte("value1"), 0)
	c.Set("key2", []byte("value2"), 0)

	stats = c.Stats()
	if stats["curr_items"] != "2" {
		t.Errorf("Expected 2 items, got %s", stats["curr_items"])
	}
}

func TestExpiry(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Set a key with short TTL (200ms soft, 400ms hard with 2.0 multiplier)
	cas, setErr := c.Set("expiry_key", []byte("expiry_value"), 200*time.Millisecond)
	if setErr != nil {
		t.Fatalf("Set failed: %v", setErr)
	}
	if cas == 0 {
		t.Error("Expected non-zero CAS")
	}

	// Should be accessible immediately with flags=0 (fresh)
	val, _, flags, err := c.Get("expiry_key")
	if err != nil {
		t.Fatalf("Key should be accessible immediately: err=%v", err)
	}
	if string(val) != "expiry_value" {
		t.Errorf("Expected 'expiry_value', got '%s'", val)
	}
	if flags != 0 {
		t.Errorf("Expected flags=0 (fresh) immediately after set, got %d", flags)
	}

	// Wait past soft-expiry (200ms) but before hard-expiry (400ms)
	time.Sleep(250 * time.Millisecond)

	// First stale access should return flags=3 (refresh)
	val, _, flags, err = c.Get("expiry_key")
	if err != nil {
		t.Fatalf("Key should still be accessible after soft-expiry: err=%v", err)
	}
	if string(val) != "expiry_value" {
		t.Errorf("Expected 'expiry_value', got '%s'", val)
	}
	if flags != 3 {
		t.Errorf("Expected flags=3 (refresh) on first stale access, got %d", flags)
	}

	// Second stale access should return flags=1 (stale, not refresh)
	val, _, flags, err = c.Get("expiry_key")
	if err != nil {
		t.Fatalf("Key should still be accessible after soft-expiry: err=%v", err)
	}
	if string(val) != "expiry_value" {
		t.Errorf("Expected 'expiry_value', got '%s'", val)
	}
	if flags != 1 {
		t.Errorf("Expected flags=1 (stale) on subsequent access, got %d", flags)
	}

	// Wait past hard-expiry (400ms total from set)
	time.Sleep(200 * time.Millisecond)

	// Should be gone after hard-expiry (ErrKeyNotFound)
	_, _, _, err = c.Get("expiry_key")
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound after hard-expiry, got %v", err)
	}
}

func TestLargeValue(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Test with 1KB value
	val1K := make([]byte, 1024)
	for i := range val1K {
		val1K[i] = byte(i % 256)
	}

	cas, err := c.Set("key1k", val1K, 0)
	if err != nil {
		t.Fatalf("Set 1K value failed: %v", err)
	}
	if cas == 0 {
		t.Error("Expected non-zero CAS")
	}

	retrieved, _, _, err := c.Get("key1k")
	if err != nil {
		t.Fatalf("Get 1K value failed: %v", err)
	}
	if len(retrieved) != 1024 {
		t.Errorf("Expected 1024 bytes, got %d", len(retrieved))
	}
	for i := 0; i < 1024; i++ {
		if retrieved[i] != byte(i%256) {
			t.Errorf("Byte mismatch at %d: expected %d, got %d", i, byte(i%256), retrieved[i])
			break
		}
	}

	// Test with 10KB value
	val10K := make([]byte, 10*1024)
	for i := range val10K {
		val10K[i] = byte((i * 7) % 256)
	}

	_, err = c.Set("key10k", val10K, 0)
	if err != nil {
		t.Fatalf("Set 10K value failed: %v", err)
	}

	retrieved, _, _, err = c.Get("key10k")
	if err != nil {
		t.Fatalf("Get 10K value failed: %v", err)
	}
	if len(retrieved) != 10*1024 {
		t.Errorf("Expected 10240 bytes, got %d", len(retrieved))
	}
}

func TestMultipleKeys(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Write 100 items
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("key%02d", i)
		value := []byte("value" + key)
		if _, err := c.Set(key, value, 0); err != nil {
			t.Fatalf("Set failed for %s: %v", key, err)
		}
	}

	// Verify all items exist
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("key%02d", i)
		expected := "value" + key
		val, _, _, err := c.Get(key)
		if err != nil {
			t.Errorf("Get failed for %s: %v", key, err)
			continue
		}
		if string(val) != expected {
			t.Errorf("Value mismatch for %s: expected '%s', got '%s'", key, expected, val)
		}
	}

	// Verify stats
	stats := c.Stats()
	if stats["curr_items"] != "100" {
		t.Errorf("Expected 100 items, got %s", stats["curr_items"])
	}
}

func TestOverwrite(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Set initial value
	cas1, _ := c.Set("overwrite_key", []byte("initial"), 0)

	// Overwrite with new value
	cas2, _ := c.Set("overwrite_key", []byte("updated"), 0)

	// CAS should change
	if cas1 == cas2 {
		t.Error("CAS should change on overwrite")
	}

	// Value should be updated
	val, _, _, _ := c.Get("overwrite_key")
	if string(val) != "updated" {
		t.Errorf("Expected 'updated', got '%s'", val)
	}

	// Stats should show only 1 item
	stats := c.Stats()
	if stats["curr_items"] != "1" {
		t.Errorf("Expected 1 item after overwrite, got %s", stats["curr_items"])
	}
}

func TestKeyWithNullBytes(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Create a key with null bytes (3 null bytes)
	keyWithNulls := "\x00\x00\x00"
	value := []byte("value for null key")

	// Set should work
	_, err := c.Set(keyWithNulls, value, 0)
	if err != nil {
		t.Fatalf("Set with null byte key failed: %v", err)
	}

	// Get should return the same value
	retrieved, _, _, err := c.Get(keyWithNulls)
	if err != nil {
		t.Fatalf("Get with null byte key failed: %v", err)
	}
	if string(retrieved) != string(value) {
		t.Errorf("Expected %q, got %q", value, retrieved)
	}

	// A different key (e.g., 4 nulls) should not match
	_, _, _, err = c.Get("\x00\x00\x00\x00")
	if err != ErrKeyNotFound {
		t.Error("Different null-byte key should not match")
	}
}

func TestValueWithBinaryData(t *testing.T) {
	c, cleanup := setupTestCache(t)
	defer cleanup()

	// Create a value with binary data including nulls
	binaryValue := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0x00, 0x00}

	_, err := c.Set("binary_key", binaryValue, 0)
	if err != nil {
		t.Fatalf("Set with binary value failed: %v", err)
	}

	retrieved, _, _, err := c.Get("binary_key")
	if err != nil {
		t.Fatalf("Get with binary value failed: %v", err)
	}

	if len(retrieved) != len(binaryValue) {
		t.Errorf("Length mismatch: expected %d, got %d", len(binaryValue), len(retrieved))
	}

	for i, b := range binaryValue {
		if retrieved[i] != b {
			t.Errorf("Byte mismatch at %d: expected 0x%02x, got 0x%02x", i, b, retrieved[i])
		}
	}
}
