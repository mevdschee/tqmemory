package server

import (
	"bufio"
	"io"
	"log"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/mevdschee/tqmemory/pkg/tqmemory"
)

// Server represents the TQMemory network server.
type Server struct {
	cache          tqmemory.CacheInterface
	addr           string
	maxConnections int32
	currConns      int32
}

// New creates a new Server instance.
func New(cache tqmemory.CacheInterface, addr string) *Server {
	return &Server{
		cache:          cache,
		addr:           addr,
		maxConnections: 1024, // memcached default
	}
}

// NewWithOptions creates a new Server with options.
func NewWithOptions(cache tqmemory.CacheInterface, addr string, maxConnections int) *Server {
	return &Server{
		cache:          cache,
		addr:           addr,
		maxConnections: int32(maxConnections),
	}
}

// Start runs the server (TCP or Unix socket based on address).
func (s *Server) Start() error {
	// Determine network type based on address
	network := "tcp"
	if len(s.addr) > 0 && s.addr[0] == '/' {
		network = "unix"
		// Remove existing socket file if present
		os.Remove(s.addr)
	}

	ln, err := net.Listen(network, s.addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	log.Printf("Listening on %s %s (max connections: %d)", network, s.addr, s.maxConnections)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}

		// Check connection limit
		curr := atomic.LoadInt32(&s.currConns)
		if curr >= s.maxConnections {
			log.Printf("Connection limit reached (%d), rejecting %s", s.maxConnections, conn.RemoteAddr())
			conn.Close()
			continue
		}

		atomic.AddInt32(&s.currConns, 1)
		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer func() {
		conn.Close()
		atomic.AddInt32(&s.currConns, -1)
	}()

	// Enable TCP_NODELAY to disable Nagle's algorithm for lower latency
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
	}

	// Use 64KB read buffer to match write buffer
	reader := bufio.NewReaderSize(conn, 65536)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	firstByte, err := reader.Peek(1)
	if err != nil {
		if err != io.EOF {
			log.Printf("Peek error from %s: %v", conn.RemoteAddr(), err)
		}
		return
	}
	conn.SetReadDeadline(time.Time{}) // Reset deadline

	// Use buffered writer for all responses (64KB buffer for better batching)
	writer := bufio.NewWriterSize(conn, 65536)

	if firstByte[0] == 0x80 {
		s.handleBinary(conn, reader, writer)
	} else {
		s.handleText(conn, reader, writer)
	}
}

// CurrentConnections returns the current number of connections.
func (s *Server) CurrentConnections() int {
	return int(atomic.LoadInt32(&s.currConns))
}
