// Package timerwheel implements the per-loop hierarchical timer wheel used for
// connection deadlines.
//
// The wheel has four levels of 64 slots and a 10 ms tick. A connection owns one
// node per active deadline kind, acquired only while that deadline is armed; an
// idle connection holds no timer nodes and no time.Timer.
//
// Nodes cascade from a higher level into the next lower level when that
// higher-level bucket opens. A 10 s handshake, for example, sits in level 1
// until its 640 ms window, is moved into the exact level-0 slot, and fires with
// 10 ms resolution.
//
// The wheel is not safe for concurrent use. It is owned by one loop.
package timerwheel

// TickMillis is the resolution of the wheel.
const TickMillis uint64 = 10

const (
	// Levels is the number of wheel levels.
	Levels = 4
	// Slots is the number of buckets per level.
	Slots = 64
	// NoNode is the null node handle.
	NoNode uint32 = ^uint32(0)
	// DefaultMaxNodes bounds the wheel unless New is given a smaller value.
	DefaultMaxNodes = 1 << 20
	// growChunk is the number of nodes allocated at a time. Larger chunks
	// avoid repeated slice copies during a large connection ramp.
	growChunk = 4096
)

// Node is one timer entry. The slab stores a stable uint32 handle for each
// deadline kind, not a pointer.
type Node struct {
	Deadline uint64
	Owner    uint32
	Kind     uint8
	Level    uint8
	Slot     uint8

	Linked bool

	bNext uint32
	bPrev uint32
	aNext uint32
	aPrev uint32
}

// Wheel is a four-level hierarchical timer wheel. The zero value is not usable;
// call New.
type Wheel struct {
	nodes []Node
	free  []uint32
	all   uint32
	max   int

	levels [Levels][Slots]uint32

	// last0 is the last level-0 bucket that was expired.
	last0 uint64
	// cascaded[l] is the last absolute bucket at level l whose nodes have been
	// moved down. Index 0 is unused.
	cascaded [Levels]uint64

	scratch []uint32
}

// New returns a wheel positioned at now (milliseconds on the loop's monotonic
// clock). maxNodes bounds the number of live nodes; zero uses DefaultMaxNodes.
func New(now uint64, maxNodes int) *Wheel {
	if maxNodes <= 0 {
		maxNodes = DefaultMaxNodes
	}
	w := &Wheel{all: NoNode, max: maxNodes}
	for i := range w.levels {
		for j := range w.levels[i] {
			w.levels[i][j] = NoNode
		}
	}
	w.last0 = now / TickMillis
	for level := 1; level < Levels; level++ {
		w.cascaded[level] = now / bucketSize(level)
	}
	return w
}

func pow64(level int) uint64 {
	out := uint64(1)
	for i := 0; i < level; i++ {
		out *= Slots
	}
	return out
}

func bucketSize(level int) uint64 {
	return TickMillis * pow64(level)
}

// Acquire returns a node handle with no deadline. The caller sets Owner and
// Kind with SetMeta and arms it with Add.
func (w *Wheel) Acquire() (uint32, bool) {
	if n := len(w.free); n > 0 {
		h := w.free[n-1]
		w.free = w.free[:n-1]
		w.nodes[h] = emptyNode()
		return h, true
	}
	if len(w.nodes) >= w.max {
		return NoNode, false
	}
	if len(w.nodes) == cap(w.nodes) {
		next := make([]Node, len(w.nodes), len(w.nodes)+growChunk)
		copy(next, w.nodes)
		w.nodes = next
	}
	h := uint32(len(w.nodes))
	w.nodes = append(w.nodes, emptyNode())
	return h, true
}

func emptyNode() Node {
	return Node{bNext: NoNode, bPrev: NoNode, aNext: NoNode, aPrev: NoNode}
}

// SetMeta labels a node with its owner and kind. It must be called before Add.
func (w *Wheel) SetMeta(handle, owner uint32, kind uint8) {
	if int(handle) >= len(w.nodes) {
		return
	}
	n := &w.nodes[handle]
	n.Owner = owner
	n.Kind = kind
}

// Node returns the node for a handle. The pointer is valid only until the next
// call that can grow the node slice (Acquire). Do not retain it across calls.
func (w *Wheel) Node(handle uint32) *Node {
	if int(handle) >= len(w.nodes) {
		return nil
	}
	return &w.nodes[handle]
}

// Add arms a node at deadline (milliseconds). A deadline at or before now fires
// on the next tick. Re-arming a linked node moves it.
func (w *Wheel) Add(handle uint32, now, deadline uint64) {
	if int(handle) >= len(w.nodes) {
		return
	}
	n := &w.nodes[handle]
	if n.Linked {
		w.unlinkBucket(handle)
	} else {
		w.linkAll(handle)
		n.Linked = true
	}
	if deadline <= now {
		deadline = now + TickMillis
	}
	n.Deadline = deadline

	ticks := (deadline - now + TickMillis - 1) / TickMillis
	level := 0
	gran := uint64(1)
	for level < Levels-1 && ticks >= gran*Slots {
		level++
		gran *= Slots
	}
	bucket := deadline / (TickMillis * gran)

	forced := false
	if level == 0 {
		forced = bucket <= w.last0
	} else {
		forced = bucket <= w.cascaded[level]
	}
	if forced {
		level = 0
		bucket = now/TickMillis + 1
	}

	n.Level = uint8(level)
	n.Slot = uint8(bucket % Slots)
	w.linkBucket(handle, level, int(n.Slot))
}

// Remove disarms a node without releasing it.
func (w *Wheel) Remove(handle uint32) {
	if int(handle) >= len(w.nodes) {
		return
	}
	n := &w.nodes[handle]
	if !n.Linked {
		return
	}
	w.unlinkBucket(handle)
	w.unlinkAll(handle)
	n.Linked = false
}

// Release disarms and returns a node to the free list.
func (w *Wheel) Release(handle uint32) {
	if int(handle) >= len(w.nodes) {
		return
	}
	w.Remove(handle)
	w.nodes[handle] = emptyNode()
	w.free = append(w.free, handle)
}

// Advance processes every deadline at or before now and appends the expired
// handles to out. The caller must not use the handles after re-arming or
// releasing them.
func (w *Wheel) Advance(now uint64, out []uint32) []uint32 {
	// Cascade higher levels first so their nodes land in the level-0 buckets
	// that this call is about to expire.
	for level := Levels - 1; level >= 1; level-- {
		size := bucketSize(level)
		target := now / size
		start := w.cascaded[level] + 1
		if target < start {
			continue
		}
		if target-start > Slots*Slots {
			return w.rebuild(now, out)
		}
		for b := start; b <= target; b++ {
			w.cascaded[level] = b
			out = w.moveBucket(level, int(b%Slots), now, out)
		}
	}

	target := now / TickMillis
	start := w.last0 + 1
	if target < start {
		return out
	}
	if target-start > Slots*Slots {
		return w.rebuild(now, out)
	}
	for b := start; b <= target; b++ {
		w.last0 = b
		out = w.moveBucket(0, int(b%Slots), now, out)
	}
	return out
}

// moveBucket detaches one bucket. Expired nodes are appended to out; future
// nodes are re-added at their correct level.
func (w *Wheel) moveBucket(level, slot int, now uint64, out []uint32) []uint32 {
	h := w.levels[level][slot]
	if h == NoNode {
		return out
	}
	w.levels[level][slot] = NoNode

	w.scratch = w.scratch[:0]
	for h != NoNode {
		n := &w.nodes[h]
		next := n.bNext
		n.bNext, n.bPrev = NoNode, NoNode
		w.scratch = append(w.scratch, h)
		h = next
	}

	for _, handle := range w.scratch {
		n := &w.nodes[handle]
		if n.Deadline <= now {
			w.unlinkAll(handle)
			n.Linked = false
			out = append(out, handle)
			continue
		}
		w.Add(handle, now, n.Deadline)
	}
	return out
}

func (w *Wheel) rebuild(now uint64, out []uint32) []uint32 {
	w.scratch = w.scratch[:0]
	for h := w.all; h != NoNode; h = w.nodes[h].aNext {
		w.scratch = append(w.scratch, h)
	}
	for level := range w.levels {
		for slot := range w.levels[level] {
			w.levels[level][slot] = NoNode
		}
	}
	w.last0 = now / TickMillis
	for level := 1; level < Levels; level++ {
		w.cascaded[level] = now / bucketSize(level)
	}
	w.all = NoNode

	for _, handle := range w.scratch {
		n := &w.nodes[handle]
		n.bNext, n.bPrev = NoNode, NoNode
		n.aNext, n.aPrev = NoNode, NoNode
		n.Linked = false
		if n.Deadline <= now {
			out = append(out, handle)
			continue
		}
		w.Add(handle, now, n.Deadline)
	}
	return out
}

func (w *Wheel) linkBucket(handle uint32, level, slot int) {
	head := w.levels[level][slot]
	n := &w.nodes[handle]
	n.bNext = head
	n.bPrev = NoNode
	if head != NoNode {
		w.nodes[head].bPrev = handle
	}
	w.levels[level][slot] = handle
}

func (w *Wheel) unlinkBucket(handle uint32) {
	n := &w.nodes[handle]
	if n.bPrev != NoNode {
		w.nodes[n.bPrev].bNext = n.bNext
	} else {
		w.levels[n.Level][n.Slot] = n.bNext
	}
	if n.bNext != NoNode {
		w.nodes[n.bNext].bPrev = n.bPrev
	}
	n.bNext, n.bPrev = NoNode, NoNode
}

func (w *Wheel) linkAll(handle uint32) {
	n := &w.nodes[handle]
	n.aPrev = NoNode
	n.aNext = w.all
	if w.all != NoNode {
		w.nodes[w.all].aPrev = handle
	}
	w.all = handle
}

func (w *Wheel) unlinkAll(handle uint32) {
	n := &w.nodes[handle]
	if n.aPrev != NoNode {
		w.nodes[n.aPrev].aNext = n.aNext
	} else {
		w.all = n.aNext
	}
	if n.aNext != NoNode {
		w.nodes[n.aNext].aPrev = n.aPrev
	}
	n.aNext, n.aPrev = NoNode, NoNode
}
