// Package connslab owns connection records, generations, and the write queue.
//
// A Slot is allocated from a chunked slab and addressed by a stable local slot
// plus a generation. The generation makes stale handles safe after reuse.
//
//	slab := connslab.NewSlab(1_000_000, 512)
//	slot, gen, ok := slab.Acquire(fd)
package connslab
