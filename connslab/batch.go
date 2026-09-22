package connslab

import "sync"

// Batch is a pooled list of connection refs handed to one loop's fan-out
// command. The producer fills it; the owning loop consumes it and returns it to
// the pool. It must not be referenced by the planner after publication.
type Batch struct {
	Refs []Ref
}

var batchPool = sync.Pool{New: func() any { return &Batch{} }}

// AcquireBatch returns an empty batch with room for n refs.
func AcquireBatch(n int) *Batch {
	b := batchPool.Get().(*Batch)
	if cap(b.Refs) < n {
		b.Refs = make([]Ref, 0, n)
	} else {
		b.Refs = b.Refs[:0]
	}
	return b
}

// Release returns the batch to the pool.
func (b *Batch) Release() {
	if b == nil {
		return
	}
	b.Refs = b.Refs[:0]
	batchPool.Put(b)
}
