package event

import (
	"sync"
)

// Ack is the outcome delivered to an EmitWithAck callback. Payload aliases the
// receive buffer and is valid only for the callback.
type Ack struct {
	Event   uint32
	Payload []byte
	Err     error
}

// PackAckToken packs the pending-slab slot and generation into one opaque
// token. The low half is the slot; the high half is the generation.
func PackAckToken(slot, gen uint32) uint64 {
	return uint64(gen)<<32 | uint64(slot)
}

// UnpackAckToken splits a token from PackAckToken.
func UnpackAckToken(token uint64) (slot, gen uint32) {
	return uint32(token), uint32(token >> 32)
}

type pendingSlot struct {
	used  bool
	gen   uint32
	loop  uint16
	event uint32
	// connSlot and connGen bind the entry to the connection that created it.
	// A completion must match all three identity fields, so a peer cannot
	// complete another connection's acknowledgement with a guessed token.
	connSlot uint32
	connGen  uint32
	cb       func(Ack)
}

// Pending is the server-wide bounded slab of outstanding acknowledgements.
// Tokens are generated from slot and generation, so completion is a direct
// index rather than a map lookup.
type Pending struct {
	mu    sync.Mutex
	slots []pendingSlot
	free  []uint32
	max   int
}

// NewPending returns a slab with at most max live entries.
func NewPending(max int) *Pending {
	if max <= 0 {
		max = 1
	}
	return &Pending{max: max, free: make([]uint32, 0, min(max, 4096))}
}

// Bind reserves one slot and returns its token. The entry is bound to the
// creating connection's identity: only a completion that presents the same
// loop, slot, and generation can consume it.
func (p *Pending) Bind(loop uint16, connSlot, connGen, eventID uint32, cb func(Ack)) (token uint64, slot uint32, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var idx uint32
	if n := len(p.free); n > 0 {
		idx = p.free[n-1]
		p.free = p.free[:n-1]
	} else {
		if len(p.slots) >= p.max {
			return 0, 0, false
		}
		idx = uint32(len(p.slots))
		p.slots = append(p.slots, pendingSlot{})
	}
	s := &p.slots[idx]
	s.gen++
	if s.gen == 0 {
		s.gen = 1
	}
	s.used = true
	s.loop = loop
	s.event = eventID
	s.connSlot = connSlot
	s.connGen = connGen
	s.cb = cb
	return PackAckToken(idx, s.gen), idx, true
}

type pendingEntry struct {
	loop     uint16
	event    uint32
	callback func(Ack)
}

func (p *Pending) takeSlot(idx, gen uint32) (pendingEntry, bool) {
	if int(idx) >= len(p.slots) {
		return pendingEntry{}, false
	}
	s := &p.slots[idx]
	if !s.used || s.gen != gen {
		return pendingEntry{}, false
	}
	e := pendingEntry{loop: s.loop, event: s.event, callback: s.cb}
	s.used = false
	s.cb = nil
	p.free = append(p.free, idx)
	return e, e.callback != nil
}

// Take completes a token. It returns the stored callback exactly once, and
// only when loop, slot, and generation all match the bound entry. Any mismatch
// is a stale or forged completion and leaves the entry untouched.
func (p *Pending) Take(token uint64, loop uint16, connSlot, connGen uint32) (e AckEntry, ok bool) {
	idx, gen := UnpackAckToken(token)
	p.mu.Lock()
	if int(idx) >= len(p.slots) {
		p.mu.Unlock()
		return AckEntry{}, false
	}
	s := &p.slots[idx]
	if !s.used || s.gen != gen || s.loop != loop || s.connSlot != connSlot || s.connGen != connGen {
		p.mu.Unlock()
		return AckEntry{}, false
	}
	entry, ok := p.takeSlot(idx, gen)
	p.mu.Unlock()
	if !ok {
		return AckEntry{}, false
	}
	return AckEntry{Slot: idx, Loop: entry.loop, Event: entry.event, Callback: entry.callback}, true
}

// AckEntry is the result of completing a pending acknowledgement.
type AckEntry struct {
	Slot     uint32
	Loop     uint16
	Event    uint32
	Callback func(Ack)
}

// Timeout completes a slab entry by index and generation. It is called on the
// owning loop when the timer wheel expires. A generation mismatch means the
// timer belongs to a released entry and must not touch its rebound successor.
func (p *Pending) Timeout(slot, gen uint32) (e AckEntry, ok bool) {
	p.mu.Lock()
	if int(slot) >= len(p.slots) {
		p.mu.Unlock()
		return AckEntry{}, false
	}
	entry, ok := p.takeSlot(slot, gen)
	p.mu.Unlock()
	if !ok {
		return AckEntry{}, false
	}
	return AckEntry{Slot: slot, Loop: entry.loop, Event: entry.event, Callback: entry.callback}, true
}

// Len reports the number of live entries.
func (p *Pending) Len() int {
	p.mu.Lock()
	n := len(p.slots) - len(p.free)
	p.mu.Unlock()
	return n
}
