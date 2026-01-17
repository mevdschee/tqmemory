package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mevdschee/tqmemory/pkg/tqmemory"
)

func main() {
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to file")
	duration := flag.Int("duration", 5, "benchmark duration in seconds")
	clients := flag.Int("clients", 10, "number of concurrent clients")
	keys := flag.Int("keys", 10000, "number of keys")
	valueSize := flag.Int("size", 1024, "value size in bytes")
	flag.Parse()

	// Create cache
	cfg := tqmemory.DefaultConfig()
	cfg.MaxMemory = 256 * 1024 * 1024
	cache, err := tqmemory.NewSharded(cfg, 4)
	if err != nil {
		log.Fatal(err)
	}
	defer cache.Close()

	// Populate cache
	value := make([]byte, *valueSize)
	for i := 0; i < *keys; i++ {
		key := fmt.Sprintf("key%d", i)
		cache.Set(key, value, 0)
	}
	fmt.Printf("Populated %d keys with %d byte values\n", *keys, *valueSize)

	// Pre-generate keys to avoid fmt.Sprintf in hot path
	keyList := make([]string, *keys)
	for i := 0; i < *keys; i++ {
		keyList[i] = fmt.Sprintf("key%d", i)
	}

	// Start CPU profiling if requested
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	// Run benchmark
	var wg sync.WaitGroup
	var totalGets int64
	stopChan := make(chan struct{})
	startTime := time.Now()

	for i := 0; i < *clients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			var localGets int64
			keyNum := clientID
			numKeys := len(keyList)
			for {
				select {
				case <-stopChan:
					atomic.AddInt64(&totalGets, localGets)
					return
				default:
					key := keyList[keyNum%numKeys]
					_, _, _ = cache.Get(key)
					localGets++
					keyNum++
				}
			}
		}(i)
	}

	// Run for specified duration
	time.Sleep(time.Duration(*duration) * time.Second)
	close(stopChan)
	wg.Wait()
	elapsed := time.Since(startTime)

	gets := atomic.LoadInt64(&totalGets)
	rps := float64(gets) / elapsed.Seconds()
	fmt.Printf("Completed %d GETs in %v (%.2f req/sec)\n", gets, elapsed, rps)
	if *cpuprofile != "" {
		fmt.Printf("CPU profile written to: %s\n", *cpuprofile)
	}
}
