package room

import (
	"sync"

	connection "github.com/encryptedbusiness/encryl/connslab"
)

// Group is one loop's share of a fan-out.
type Group struct {
	Loop uint16
	Refs []connection.Ref
}

// Planner groups connection refs by their owning loop. One planner is reused
// across Emits, so a fan-out does not allocate a map per call. It is not safe
// for concurrent use.
type Planner struct {
	groups map[uint16][]connection.Ref
	order  []uint16
	out    []Group
}

var plannerPool = sync.Pool{New: func() any {
	return &Planner{groups: make(map[uint16][]connection.Ref, 8)}
}}

// AcquirePlanner returns a reset planner.
func AcquirePlanner() *Planner {
	p := plannerPool.Get().(*Planner)
	p.Reset()
	return p
}

// Release returns the planner to the pool.
func (p *Planner) Release() {
	p.Reset()
	plannerPool.Put(p)
}

// Reset clears all groups while keeping their capacity.
func (p *Planner) Reset() {
	for _, loop := range p.order {
		p.groups[loop] = p.groups[loop][:0]
	}
	p.order = p.order[:0]
	p.out = p.out[:0]
}

// Add assigns a ref to its loop's group. An empty group means the loop has
// not been seen since the last Reset; a non-empty group is already ordered.
func (p *Planner) Add(ref connection.Ref) {
	g := p.groups[ref.Loop]
	if len(g) == 0 {
		p.order = append(p.order, ref.Loop)
	}
	p.groups[ref.Loop] = append(g, ref)
}

// Groups returns the per-loop groups in first-seen order. The slice and its
// contents are valid until Reset or Release.
func (p *Planner) Groups() []Group {
	p.out = p.out[:0]
	for _, loop := range p.order {
		p.out = append(p.out, Group{Loop: loop, Refs: p.groups[loop]})
	}
	return p.out
}

// Empty reports whether the planner holds no refs.
func (p *Planner) Empty() bool { return len(p.order) == 0 }
