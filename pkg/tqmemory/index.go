package tqmemory

import (
	"errors"
)

// Common errors
var (
	ErrKeyNotFound   = errors.New("key not found")
	ErrKeyTooLarge   = errors.New("key too large")
	ErrValueTooLarge = errors.New("value too large")
	ErrKeyExists     = errors.New("key already exists")
	ErrCasMismatch   = errors.New("cas mismatch")
	ErrNotNumeric    = errors.New("cannot increment or decrement non-numeric value")
)
