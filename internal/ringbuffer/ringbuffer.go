// Package ringbuffer provides a generic, thread-safe ring buffer.
package ringbuffer

import "sync"

// RingBuffer is a fixed-capacity, thread-safe ring buffer.
// When full, the oldest entry is overwritten.
type RingBuffer[T any] struct {
	mu   sync.RWMutex
	data []T
	head int
	size int
	cap  int
}

// New creates a new RingBuffer with the given capacity.
// Capacity must be > 0.
func New[T any](capacity int) *RingBuffer[T] {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer[T]{
		data: make([]T, capacity),
		cap:  capacity,
	}
}

// Push adds an item to the buffer. If full, the oldest entry is overwritten.
func (r *RingBuffer[T]) Push(item T) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.data[r.head] = item
	r.head = (r.head + 1) % r.cap
	if r.size < r.cap {
		r.size++
	}
}

// Snapshot returns a copy of all entries in oldest-to-newest order.
func (r *RingBuffer[T]) Snapshot() []T {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]T, r.size)
	if r.size == 0 {
		return out
	}
	start := (r.head - r.size + r.cap) % r.cap
	for i := 0; i < r.size; i++ {
		out[i] = r.data[(start+i)%r.cap]
	}
	return out
}

// Len returns the current number of entries.
func (r *RingBuffer[T]) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.size
}

// Cap returns the maximum capacity.
func (r *RingBuffer[T]) Cap() int {
	return r.cap
}

// Clear removes all entries.
func (r *RingBuffer[T]) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.head = 0
	r.size = 0
}
