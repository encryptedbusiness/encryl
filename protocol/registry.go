package protocol

import (
	"errors"
	"sync"
	"sync/atomic"
)

// ErrInvalidEvent reports an empty or zero Event registration.
var ErrInvalidEvent = errors.New("protocol: invalid event registration")

// EventRegistry maps event names to numeric IDs. Registration happens before
// traffic; lookups take a read lock. The hot path uses numeric IDs only.
type EventRegistry struct {
	mu     sync.RWMutex
	byName map[string]uint32
	byID   map[uint32]string
	frozen atomic.Pointer[map[string]uint32]
}

// NewEventRegistry returns an empty registry.
func NewEventRegistry() *EventRegistry {
	return &EventRegistry{
		byName: make(map[string]uint32, 32),
		byID:   make(map[uint32]string, 32),
	}
}

// Register adds one name and ID. A repeated identical registration is a no-op.
func (r *EventRegistry) Register(name string, id uint32) error {
	if id == 0 || name == "" {
		return ErrInvalidEvent
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen.Load() != nil {
		return errors.New("protocol: event registry is frozen")
	}
	if existing, ok := r.byName[name]; ok {
		if existing == id {
			return nil
		}
		return ErrInvalidEvent
	}
	if other, ok := r.byID[id]; ok && other != name {
		return ErrInvalidEvent
	}
	r.byName[name] = id
	r.byID[id] = name
	return nil
}

// ID resolves a name. It is safe to call after the registry is frozen.
func (r *EventRegistry) ID(name string) (uint32, bool) {
	if snapshot := r.frozen.Load(); snapshot != nil {
		id, ok := (*snapshot)[name]
		return id, ok
	}
	r.mu.RLock()
	id, ok := r.byName[name]
	r.mu.RUnlock()
	return id, ok
}

// IDBytes resolves a name from a borrowed slice. The string conversion in the
// map lookup does not allocate.
func (r *EventRegistry) IDBytes(name []byte) (uint32, bool) {
	if snapshot := r.frozen.Load(); snapshot != nil {
		id, ok := (*snapshot)[string(name)]
		return id, ok
	}
	r.mu.RLock()
	id, ok := r.byName[string(name)]
	r.mu.RUnlock()
	return id, ok
}

// Freeze takes an immutable snapshot of the name table. Registration after
// Freeze fails. Lookups then perform one atomic load and one map lookup.
func (r *EventRegistry) Freeze() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen.Load() != nil {
		return
	}
	snapshot := make(map[string]uint32, len(r.byName))
	for name, id := range r.byName {
		snapshot[name] = id
	}
	r.frozen.Store(&snapshot)
}
