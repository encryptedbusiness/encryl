package reactor

import "sync"

// mpscRing is a fixed-capacity multi-producer, single-consumer queue.
// Producers on other goroutines push; the owning loop pops. A full ring is
// backpressure, never a block.
type mpscRing[T any] struct {
	mu   sync.Mutex
	buf  []T
	head int
	size int
	wake func()

	dropped uint64
}

func newMPSCRing[T any](capacity, fallback int) *mpscRing[T] {
	if capacity <= 0 {
		capacity = fallback
	}
	return &mpscRing[T]{buf: make([]T, capacity)}
}

// SetWake installs a wake callback.
func (r *mpscRing[T]) SetWake(wake func()) {
	r.mu.Lock()
	r.wake = wake
	r.mu.Unlock()
}

// Push appends one item. It returns false when the ring is full.
func (r *mpscRing[T]) Push(v T) bool {
	r.mu.Lock()
	if r.size == len(r.buf) {
		r.dropped++
		r.mu.Unlock()
		return false
	}
	idx := (r.head + r.size) % len(r.buf)
	r.buf[idx] = v
	r.size++
	wake := r.wake
	shouldWake := r.size == 1 && wake != nil
	r.mu.Unlock()

	if shouldWake {
		wake()
	}
	return true
}

// Pop removes one item.
func (r *mpscRing[T]) Pop() (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var zero T
	if r.size == 0 {
		return zero, false
	}
	v := r.buf[r.head]
	r.buf[r.head] = zero
	r.head = (r.head + 1) % len(r.buf)
	r.size--
	return v, true
}

// Len reports the number of queued items.
func (r *mpscRing[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Capacity reports the fixed ring size.
func (r *mpscRing[T]) Capacity() int { return len(r.buf) }

// Dropped reports pushes rejected because the ring was full.
func (r *mpscRing[T]) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}
