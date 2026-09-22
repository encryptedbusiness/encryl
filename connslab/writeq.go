package connslab

import (
	"sync"
	"sync/atomic"

	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/ws"
)

// NewFrameWithHeader allocates a frame with room for n payload bytes and a
// WebSocket binary header. It returns the frame and the header length, so a
// codec can write directly into f.Buf[h:].
func NewFrameWithHeader(n int) (*Frame, int) {
	hl := ws.FrameHeaderLen(n)
	f := NewFrame(hl + n)
	h := ws.AppendFrameHeader(f.Buf, ws.Binary, n)
	return f, h
}

// BuildFrame encodes one WebSocket binary frame containing the v2 envelope and
// the payload. The frame starts with one reference owned by the caller.
func BuildFrame(kind event.Kind, eventID uint32, requestID uint64, flags uint16, payload []byte) *Frame {
	n := event.HeaderSize + len(payload)
	hl := ws.FrameHeaderLen(n)
	f := NewFrame(hl + n)
	h := ws.AppendFrameHeader(f.Buf, ws.Binary, n)
	event.Encode(f.Buf[h:h+event.HeaderSize], event.Envelope{
		Kind:      kind,
		Flags:     flags,
		Event:     eventID,
		RequestID: requestID,
	})
	copy(f.Buf[h+event.HeaderSize:], payload)
	f.Counted = true
	return f
}

// BackpressurePolicy decides what happens when a connection's bounded write
// queue cannot accept another message.
type BackpressurePolicy int

const (
	// BackpressureReject returns can't-admit to the caller and leaves the
	// connection open.
	BackpressureReject BackpressurePolicy = iota
	// BackpressureDropNewest discards the new frame.
	BackpressureDropNewest
	// BackpressureDropOldest evicts the oldest queued frame.
	BackpressureDropOldest
	// BackpressureCloseSlow closes the connection as a slow consumer.
	BackpressureCloseSlow
)

// AdmitResult reports the outcome of a synchronous admission attempt.
type AdmitResult uint8

const (
	// AdmitQueued means the frame reserved write budget.
	AdmitQueued AdmitResult = iota
	// AdmitDropped means the policy discarded the new frame.
	AdmitDropped
	// AdmitRejected means the policy rejected the send synchronously.
	AdmitRejected
	// AdmitCloseSlow means the connection must be closed as a slow consumer.
	AdmitCloseSlow
)

// Accounting is the cross-goroutine admission gate for one connection. It is
// a small mutex-protected counter pair: byte and message limits must move
// together, and a pair of atomics cannot be updated atomically.
type Accounting struct {
	mu    sync.Mutex
	bytes int64
	msgs  int32
	evict bool
}

// Admit reserves budget for one frame. Queued returns true when the caller may
// hand the frame to the owning loop; the reservation must later be released by
// the loop exactly once (on send, drop, eviction, or close).
func (a *Accounting) Admit(cost int32, maxMsgs int32, maxBytes int64, policy BackpressurePolicy) AdmitResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	if maxMsgs > 0 && maxBytes > 0 && a.msgs < maxMsgs && a.bytes+int64(cost) <= maxBytes {
		a.msgs++
		a.bytes += int64(cost)
		return AdmitQueued
	}
	switch policy {
	case BackpressureDropNewest:
		return AdmitDropped
	case BackpressureDropOldest:
		if !a.evict {
			a.evict = true
			a.msgs++
			a.bytes += int64(cost)
			return AdmitQueued
		}
		return AdmitDropped
	case BackpressureCloseSlow:
		return AdmitCloseSlow
	default:
		return AdmitRejected
	}
}

// Add records budget for a frame that the loop enqueued directly (fan-out).
func (a *Accounting) Add(cost int32) {
	a.mu.Lock()
	a.msgs++
	a.bytes += int64(cost)
	a.mu.Unlock()
}

// Release returns budget for one frame.
func (a *Accounting) Release(cost int32) {
	a.mu.Lock()
	if a.msgs > 0 {
		a.msgs--
	}
	a.bytes -= int64(cost)
	if a.bytes < 0 {
		a.bytes = 0
	}
	a.mu.Unlock()
}

// TakeEvict reports and clears a pending drop-oldest request.
func (a *Accounting) TakeEvict() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.evict
	a.evict = false
	return v
}

// Pending reports the reserved message and byte counts. It is a metric and
// test hook.
func (a *Accounting) Pending() (msgs int32, bytes int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.msgs, a.bytes
}

// Reset clears all accounting. The owning loop calls it when a slot is reused.
func (a *Accounting) Reset() {
	a.mu.Lock()
	a.msgs, a.bytes, a.evict = 0, 0, false
	a.mu.Unlock()
}

// bufferClasses are the pooled frame buffer capacities.
var bufferClasses = [...]int{256, 1024, 4096, 16384, 65536}

// bufferPools are fixed-size channels of reusable buffers. A channel copies
// the 24-byte slice header into its ring without boxing it on the heap, so a
// Get/Put pair allocates nothing. sync.Pool would box every slice header.
var bufferPools [len(bufferClasses)]chan []byte

func init() {
	for i := range bufferPools {
		bufferPools[i] = make(chan []byte, 1024)
	}
}

func bufferClass(n int) int {
	for i, c := range bufferClasses {
		if n <= c {
			return i
		}
	}
	return -1
}

func getBuffer(n int) ([]byte, int) {
	ci := bufferClass(n)
	if ci < 0 {
		return make([]byte, n), -1
	}
	select {
	case b := <-bufferPools[ci]:
		if cap(b) >= n {
			return b[:n], ci
		}
	default:
	}
	return make([]byte, n, bufferClasses[ci]), ci
}

func putBuffer(b []byte, ci int) {
	if ci < 0 || cap(b) == 0 {
		return
	}
	b = b[:0]
	select {
	case bufferPools[ci] <- b:
	default:
	}
}

// Frame is one reference-counted encoded WebSocket frame. A unicast send owns
// one reference; a fan-out shares one Frame across every recipient.
type Frame struct {
	// Buf is the complete wire frame: WebSocket header plus envelope plus
	// payload. It is read-only once published.
	Buf []byte
	// Cost is the number of write-queue bytes this frame accounts for.
	Cost int32
	// CloseAfter asks the loop to close the connection after this frame is
	// flushed.
	CloseAfter bool
	// CloseCode is the close status used with CloseAfter.
	CloseCode uint16
	// Admitted reports that the cross-goroutine accounting already reserved
	// this frame's budget.
	Admitted bool
	// Counted reports that the frame participates in the write budget. Control
	// frames (close, pong) are not counted.
	Counted bool
	// Volatile frames are dropped instead of queued when the connection is busy.
	Volatile bool

	refs  atomic.Int32
	class int8
}

var framePool = sync.Pool{New: func() any { return new(Frame) }}

// NewFrame allocates a frame with room for n encoded bytes and one reference.
func NewFrame(n int) *Frame {
	buf, class := getBuffer(n)
	f := framePool.Get().(*Frame)
	f.Buf = buf
	f.Cost = int32(n)
	f.CloseAfter = false
	f.CloseCode = 0
	f.Admitted = false
	f.Counted = false
	f.Volatile = false
	f.class = int8(class)
	f.refs.Store(1)
	return f
}

// Retain takes one additional reference.
func (f *Frame) Retain() { f.refs.Add(1) }

// Release drops one reference and returns the frame to its pool at zero.
func (f *Frame) Release() {
	if f.refs.Add(-1) != 0 {
		return
	}
	putBuffer(f.Buf, int(f.class))
	*f = Frame{}
	framePool.Put(f)
}

// Refs reports the current reference count. It is a test hook.
func (f *Frame) Refs() int32 { return f.refs.Load() }

// Node is one intrusive write-queue entry. It holds exactly one Frame
// reference until the client pops it.
type Node struct {
	Frame *Frame
	Next  *Node
}

// NodePool is a loop-owned freelist of queue nodes. It is not safe for
// concurrent use; only the owning loop allocates and recycles nodes.
type NodePool struct {
	free []*Node
}

// Get returns a recycled or fresh node.
func (p *NodePool) Get() *Node {
	if n := len(p.free); n > 0 {
		node := p.free[n-1]
		p.free = p.free[:n-1]
		node.Frame = nil
		node.Next = nil
		return node
	}
	return new(Node)
}

// Put recycles a node.
func (p *NodePool) Put(n *Node) {
	n.Frame = nil
	n.Next = nil
	if len(p.free) < 4096 {
		p.free = append(p.free, n)
	}
}

// Push appends a node to the loop-owned queue. The caller owns the node and
// transfers it.
func (s *Slot) Push(n *Node) {
	if s.extra == nil {
		s.extra = &connExtra{}
	}
	if s.extra.qTail == nil {
		s.extra.qHead, s.extra.qTail = n, n
	} else {
		s.extra.qTail.Next = n
		s.extra.qTail = n
	}
}

// Pop removes the oldest node, or returns nil.
func (s *Slot) Pop() *Node {
	if s.extra == nil {
		return nil
	}
	n := s.extra.qHead
	if n == nil {
		return nil
	}
	s.extra.qHead = n.Next
	if s.extra.qHead == nil {
		s.extra.qTail = nil
	}
	n.Next = nil
	return n
}

// QueueEmpty reports whether no frames are queued.
func (s *Slot) QueueEmpty() bool { return s.extra == nil || s.extra.qHead == nil }
