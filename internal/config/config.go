package config

import (
	"os"
	"strconv"
	"strings"
)

// Config represents the application configuration.
// Uses the same option names as memcached command-line flags.
type Config struct {
	Port        int    // -p, -port: TCP port to listen on (default: 11211)
	Listen      string // -l, -listen: Interface to listen on (default: INADDR_ANY)
	Memory      int    // -m, -memory: Max memory in megabytes (default: 64)
	Connections int    // -c, -connections: Max simultaneous connections (default: 1024)
	Threads     int    // -t, -threads: Number of threads (default: 4)
}

// DefaultConfig returns memcached-compatible defaults
func DefaultConfig() *Config {
	return &Config{
		Port:        11211,
		Listen:      "",
		Memory:      64,
		Connections: 1024,
		Threads:     4,
	}
}

// Load reads a configuration file from the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return parse(string(data))
}

func parse(data string) (*Config, error) {
	cfg := DefaultConfig()

	lines := strings.Split(data, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		// Remove inline comments
		if idx := strings.Index(value, "#"); idx != -1 {
			value = strings.TrimSpace(value[:idx])
		}

		switch key {
		case "port":
			if n, err := strconv.Atoi(value); err == nil {
				cfg.Port = n
			}
		case "listen":
			cfg.Listen = value
		case "memory":
			if n, err := strconv.Atoi(value); err == nil {
				cfg.Memory = n
			}
		case "connections":
			if n, err := strconv.Atoi(value); err == nil {
				cfg.Connections = n
			}
		case "threads":
			if n, err := strconv.Atoi(value); err == nil {
				cfg.Threads = n
			}
		}
	}

	return cfg, nil
}
