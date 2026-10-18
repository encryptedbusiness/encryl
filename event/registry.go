// Package event is the vertical slice that owns the wire envelope, the event
// handler registry, middleware chains, and pending acknowledgements.
package event

import (
	"errors"
	"sync"
	"sync/atomic"
)

// Message is one decoded application message.
type Message struct {
	// Event is the numeric event identifier from the v2 event.
	Event uint32
	// RequestID correlates a response with a request. Zero means unsolicited.
	RequestID uint64
	// Payload aliases an internal buffer and is valid only for the call.
	Payload []byte
	// Binary reports whether the peer sent a binary WebSocket frame.
	Binary bool
}

// Handler handles one inbound event.
type Handler[C any] func(c C, m Message) error

// Middleware wraps an event handler. Registration order is execution order.
type Middleware[C any] func(next Handler[C]) Handler[C]

// Registry holds the event handlers. Handlers are immutable after the server
// starts; Lookup takes a read lock.
type Registry[C any] struct {
	mu       sync.RWMutex
	handlers map[uint32]Handler[C]
	chain    []Middleware[C]
	frozen   atomic.Pointer[map[uint32]Handler[C]]
}

// NewRegistry returns an empty registry.
func NewRegistry[C any]() *Registry[C] {
	return &Registry[C]{handlers: make(map[uint32]Handler[C], 16)}
}

// On binds an event ID to a handler. Registering the same event twice returns
// an error.
func (r *Registry[C]) On(event uint32, h Handler[C]) error {
	if h == nil {
		return errors.New("event: nil handler")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen.Load() != nil {
		return errors.New("event: registry is frozen")
	}
	if r.handlers == nil {
		r.handlers = make(map[uint32]Handler[C], 16)
	}
	if _, exists := r.handlers[event]; exists {
		return errors.New("event: event already registered")
	}
	r.handlers[event] = r.compose(h)
	return nil
}

// Use appends a middleware. It must be called before the first On so the
// composed chains stay allocation-free at dispatch time.
func (r *Registry[C]) Use(mw Middleware[C]) error {
	if mw == nil {
		return errors.New("event: nil middleware")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen.Load() != nil {
		return errors.New("event: registry is frozen")
	}
	if len(r.handlers) != 0 {
		return errors.New("event: middleware must be registered before handlers")
	}
	r.chain = append(r.chain, mw)
	return nil
}

func (r *Registry[C]) compose(h Handler[C]) Handler[C] {
	for i := len(r.chain) - 1; i >= 0; i-- {
		h = r.chain[i](h)
	}
	return h
}

// Freeze takes an immutable snapshot of the composed handlers. After Freeze,
// On and Use return an error. Lookup then performs one atomic load and one map
// lookup, with no lock and no allocation per message.
func (r *Registry[C]) Freeze() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen.Load() != nil {
		return
	}
	snapshot := make(map[uint32]Handler[C], len(r.handlers))
	for event, handler := range r.handlers {
		snapshot[event] = handler
	}
	r.frozen.Store(&snapshot)
}

// Lookup returns the composed handler for an event.
func (r *Registry[C]) Lookup(event uint32) (Handler[C], bool) {
	if snapshot := r.frozen.Load(); snapshot != nil {
		h, ok := (*snapshot)[event]
		return h, ok
	}
	r.mu.RLock()
	h, ok := r.handlers[event]
	r.mu.RUnlock()
	return h, ok
}
