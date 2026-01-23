package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"

	"github.com/mevdschee/tqmemory/internal/config"
	"github.com/mevdschee/tqmemory/pkg/server"
	"github.com/mevdschee/tqmemory/pkg/tqmemory"
)

func main() {
	// Memcached-compatible short flags
	port := flag.Int("p", 11211, "TCP port to listen on")
	listenAddr := flag.String("l", "", "Interface to listen on (default: INADDR_ANY)")
	socketPath := flag.String("s", "", "Unix socket path (overrides -p and -l)")
	memory := flag.Int("m", 64, "Max memory to use for items in megabytes")
	connections := flag.Int("c", 1024, "Max simultaneous connections")
	threads := flag.Int("t", 4, "Number of threads to use")

	// Long name alternatives (same variables)
	flag.IntVar(port, "port", 11211, "TCP port to listen on")
	flag.StringVar(listenAddr, "listen", "", "Interface to listen on")
	flag.StringVar(socketPath, "socket", "", "Unix socket path")
	flag.IntVar(memory, "memory", 64, "Max memory in megabytes")
	flag.IntVar(connections, "connections", 1024, "Max simultaneous connections")
	flag.IntVar(threads, "threads", 4, "Number of threads")

	// TQMemory-specific options (not in memcached)
	staleMultiplier := flag.Float64("stale", 2.0, "Stale multiplier (hard TTL = soft TTL × this, 0 to disable)")
	configFile := flag.String("config", "", "Path to config file")
	pprofEnabled := flag.Bool("pprof", false, "Enable pprof profiling server on :6062")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nTQMemory - High-performance memcached replacement\n\n")
		fmt.Fprintf(os.Stderr, "Memcached-compatible options:\n")
		fmt.Fprintf(os.Stderr, "  -p, -port <num>          TCP port to listen on (default: 11211)\n")
		fmt.Fprintf(os.Stderr, "  -l, -listen <addr>       Interface to listen on (default: INADDR_ANY)\n")
		fmt.Fprintf(os.Stderr, "  -s, -socket <path>       Unix socket path (overrides -p and -l)\n")
		fmt.Fprintf(os.Stderr, "  -m, -memory <num>        Max memory in megabytes (default: 64)\n")
		fmt.Fprintf(os.Stderr, "  -c, -connections <num>   Max simultaneous connections (default: 1024)\n")
		fmt.Fprintf(os.Stderr, "  -t, -threads <num>       Number of threads (default: 4)\n")
		fmt.Fprintf(os.Stderr, "\nTQMemory options:\n")
		fmt.Fprintf(os.Stderr, "  -stale <num>             Stale multiplier (default: 2.0, 0 to disable)\n")
		fmt.Fprintf(os.Stderr, "  -config <file>           Path to config file\n")
		fmt.Fprintf(os.Stderr, "  -pprof                   Enable pprof profiling server on :6062\n")
	}
	flag.Parse()

	var cfg tqmemory.Config
	var listenString string
	var threadCount int
	var maxConnections int

	// Load config file if specified
	if *configFile != "" {
		fileCfg, err := config.Load(*configFile)
		if err != nil {
			log.Fatalf("Failed to load config file: %v", err)
		}
		// Build listen string from config
		if fileCfg.Listen != "" {
			listenString = fmt.Sprintf("%s:%d", fileCfg.Listen, fileCfg.Port)
		} else {
			listenString = fmt.Sprintf(":%d", fileCfg.Port)
		}
		cfg = tqmemory.DefaultConfig()
		cfg.MaxMemory = int64(fileCfg.Memory) * 1024 * 1024
		threadCount = fileCfg.Threads
		maxConnections = fileCfg.Connections
		log.Printf("Loaded config from %s", *configFile)
		// Apply stale multiplier from config file
		cfg.StaleMultiplier = fileCfg.StaleMultiplier
	} else {
		// Use command-line flags
		if *socketPath != "" {
			listenString = *socketPath
		} else if *listenAddr != "" {
			listenString = fmt.Sprintf("%s:%d", *listenAddr, *port)
		} else {
			listenString = fmt.Sprintf(":%d", *port)
		}
		cfg = tqmemory.DefaultConfig()
		cfg.MaxMemory = int64(*memory) * 1024 * 1024
		cfg.StaleMultiplier = *staleMultiplier
		threadCount = *threads
		maxConnections = *connections
	}

	cache, err := tqmemory.NewSharded(cfg, threadCount)
	if err != nil {
		log.Fatalf("Failed to initialize TQMemory: %v", err)
	}
	defer cache.Close()

	// Use standard networking (io_uring is experimental)
	srv := server.NewWithOptions(cache, listenString, maxConnections)
	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Start pprof server if enabled
	if *pprofEnabled {
		go func() {
			log.Println("Starting pprof server on :6062")
			if err := http.ListenAndServe("localhost:6062", nil); err != nil {
				log.Println("Pprof failed:", err)
			}
		}()
	}

	// Set up signal handling
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	log.Printf("TQMemory started on %s (threads: %d, memory: %dMB, connections: %d)",
		listenString, threadCount, cfg.MaxMemory/(1024*1024), maxConnections)
	<-quit
	log.Println("Shutting down TQMemory...")
}
