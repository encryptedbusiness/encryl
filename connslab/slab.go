// Package connection is the vertical slice that owns connection identity,
// lifecycle state, and the bounded per-connection outbound queue.
package connslab

import (
	upgrade "github.com/encryptedbusiness/encryl/httpx"
	"github.com/encryptedbusiness/encryl/timerwheel"
	"github.com/encryptedbusiness/encryl/ws"
)

// Ref identifies a connection on a specific loop.
type Ref struct {
	Loop uint16
	Slot uint32
	Gen  uint32
}

// ID is the public connection identity. Slot is the global slot
// (loop*perLoop + local slot).
type ID struct {
	Slot uint32
	Gen  uint32
}

// State is a connection's lifecycle state.
type State uint8

const (
	StateFree State = iota
	StateAccepted
	StateHandshaking
	StateAuthenticating
	StateOpen
	StateDraining
	StateClosing
	StateClosed
)

func (s State) String() string {
	switch s {
	case StateFree:
		return "free"
	case StateAccepted:
		return "accepted"
	case StateHandshaking:
		return "handshaking"
	case StateAuthenticating:
		return "authenticating"
	case StateOpen:
		return "open"
	case StateDraining:
		return "draining"
	case StateClosing:
		return "closing"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// TimerKind identifies one connection deadline. A connection has at most one
// active node per kind.
type TimerKind uint8

const (
	TimerHandshake TimerKind = iota
	TimerIdle
	TimerPing
	TimerClose
	NumTimerKinds
)

func (k TimerKind) String() string {
	switch k {
	case TimerHandshake:
		return "handshake"
	case TimerIdle:
		return "idle"
	case TimerPing:
		return "ping"
	case TimerClose:
		return "close"
	default:
		return "unknown"
	}
}

// connExtra holds the optional buffers a connection needs only for bytes
// pipelined behind the upgrade request, plus the upgrade parser while the
// handshake is in flight. Most connections never allocate it.
type connExtra struct {
	pending   []byte
	owned     bool
	handshake *upgrade.Parser
	acct      *Accounting
	qHead     *Node
	qTail     *Node
}

// Slot is one connection's state. It is deliberately small: an idle connection
// holds no buffers, queues, goroutines, channels, or timers.
type Slot struct {
	FD       int32
	Gen      uint32
	InFlight uint32

	State       State
	Flags       uint8
	CloseCode   uint16
	CloseRemote bool
	// Encoding is the negotiated wire encoding: 0 native, 1 Bebop.
	Encoding uint8
	// Direct reports that FD is a direct descriptor in the loop file table.
	Direct bool
	// ReadPaused stops receive re-arming while output drains.
	ReadPaused bool

	// Timers holds the wheel handle for each deadline kind, or NoNode.
	Timers [NumTimerKinds]uint32

	// One in-flight receive and one in-flight control send at a time.
	RecvBid int32
	SendBid int32
	SendOff int32
	SendLen int32

	// SendFrame is the application frame currently on the wire, if any.
	SendFrame *Frame

	// Peer address, kept as a compact tuple; the public net.Addr is built
	// only if the application asks for it.
	Addr       [16]byte
	AddrLen    uint8
	AddrFamily uint8
	Port       uint16

	// WS is needed for the connection's lifetime. The handshake parser lives
	// in extra only while the upgrade is in flight.
	WS ws.Parser

	extra *connExtra
}

// Handshake returns the upgrade parser while the connection is handshaking.
func (s *Slot) Handshake() *upgrade.Parser {
	if s.extra == nil {
		return nil
	}
	return s.extra.handshake
}

// Acct returns the admission gate, or nil while the connection is idle.
func (s *Slot) Acct() *Accounting {
	if s.extra == nil {
		return nil
	}
	return s.extra.acct
}

// SetAcct installs or clears the admission gate.
func (s *Slot) SetAcct(a *Accounting) {
	if a == nil {
		if s.extra == nil {
			return
		}
		s.extra.acct = nil
		s.dropExtraIfIdle()
		return
	}
	if s.extra == nil {
		s.extra = &connExtra{}
	}
	s.extra.acct = a
}

func (s *Slot) dropExtraIfIdle() {
	if s.extra != nil && s.extra.handshake == nil && s.extra.acct == nil &&
		s.extra.qHead == nil && len(s.extra.pending) == 0 && !s.extra.owned {
		s.extra = nil
	}
}

// SetHandshake installs or clears the upgrade parser. Clearing drops extra
// when it holds no pipelined bytes and no admission gate.
func (s *Slot) SetHandshake(p *upgrade.Parser) {
	if p == nil {
		if s.extra == nil {
			return
		}
		s.extra.handshake = nil
		s.dropExtraIfIdle()
		return
	}
	if s.extra == nil {
		s.extra = &connExtra{}
	}
	s.extra.handshake = p
}

// Pending returns the bytes pipelined behind the upgrade request.
func (s *Slot) Pending() []byte {
	if s.extra == nil {
		return nil
	}
	return s.extra.pending
}

// SetPending stores bytes pipelined behind the upgrade request.
func (s *Slot) SetPending(b []byte) {
	if len(b) == 0 {
		if s.extra != nil {
			s.extra.pending = nil
			s.dropExtraIfIdle()
		}
		return
	}
	if s.extra == nil {
		s.extra = &connExtra{}
	}
	s.extra.pending = b
	s.extra.owned = false
}

// SetPendingOwned stores bytes whose lifetime belongs to the connection. The
// attach path uses this to avoid copying a buffer that the framework already
// read before the handoff.
func (s *Slot) SetPendingOwned(b []byte) {
	if len(b) == 0 {
		return
	}
	if s.extra == nil {
		s.extra = &connExtra{}
	}
	s.extra.pending = b
	s.extra.owned = true
}

// TakePending removes and returns the pending bytes and whether the connection
// owns their storage.
func (s *Slot) TakePending() ([]byte, bool) {
	if s.extra == nil {
		return nil, false
	}
	b := s.extra.pending
	owned := s.extra.owned
	s.extra.pending = nil
	s.extra.owned = false
	if len(b) == 0 {
		s.dropExtraIfIdle()
	}
	return b, owned
}

// Connection flags.
const (
	FlagCloseQueued uint8 = 1 << iota
	FlagPollArmed
	FlagRecvArmed
	FlagReadable
	FlagParked
	Flag101Queued
	FlagCloseAfterSend
	FlagCloseFrameQueued
)

// Has reports whether a flag is set.
func (s *Slot) Has(flag uint8) bool { return s.Flags&flag != 0 }

// Set sets a flag.
func (s *Slot) Set(flag uint8) { s.Flags |= flag }

// Clear clears a flag.
func (s *Slot) Clear(flag uint8) { s.Flags &^= flag }

// SetTo sets or clears a flag.
func (s *Slot) SetTo(flag uint8, on bool) {
	if on {
		s.Flags |= flag
	} else {
		s.Flags &^= flag
	}
}

// Slab is a chunked connection table. Slots are allocated in fixed-size chunks
// so a large MaxConnections is a ceiling, not an upfront allocation.
type Slab struct {
	chunks    [][]Slot
	chunkSize uint32
	max       uint32
	free      []uint32
	highWater uint32
	live      int
	illegal   uint64
}

// NewSlab returns a slab with a capacity ceiling and a chunk size. A zero
// chunkSize uses 4096. The ceiling is the kernel tag limit, which the
// platform kernel owns.
func NewSlab(maxConnections, chunkSize int) *Slab {
	if maxConnections <= 0 {
		maxConnections = 1
	}
	if maxConnections > int(SlotLimit) {
		maxConnections = int(SlotLimit)
	}
	if chunkSize <= 0 {
		chunkSize = 4096
	}
	if chunkSize > maxConnections {
		chunkSize = maxConnections
	}
	return &Slab{
		chunkSize: uint32(chunkSize),
		max:       uint32(maxConnections),
		free:      make([]uint32, 0, chunkSize),
	}
}

func (s *Slab) slotPtr(i uint32) *Slot {
	chunk := i / s.chunkSize
	off := i % s.chunkSize
	return &s.chunks[chunk][off]
}

func (s *Slab) resetSlot(sl *Slot) {
	sl.FD = -1
	sl.InFlight = 0
	sl.RecvBid, sl.SendBid, sl.SendOff, sl.SendLen = -1, -1, 0, 0
	sl.SendFrame = nil
	sl.Addr = [16]byte{}
	sl.AddrLen, sl.AddrFamily, sl.Port = 0, 0, 0
	sl.extra = nil
	sl.WS.Reset()
	sl.CloseCode = 0
	sl.CloseRemote = false
	sl.Encoding = 0
	sl.Direct = false
	sl.ReadPaused = false
	sl.Flags = 0
	for i := range sl.Timers {
		sl.Timers[i] = timerwheel.NoNode
	}
}

// Acquire reserves a slot for fd and returns its handle and generation.
func (s *Slab) Acquire(fd int) (slot, gen uint32, ok bool) {
	var idx uint32
	if n := len(s.free); n > 0 {
		idx = s.free[n-1]
		s.free = s.free[:n-1]
	} else {
		if s.highWater >= s.max {
			return 0, 0, false
		}
		idx = s.highWater
		s.highWater++
		chunk := idx / s.chunkSize
		if int(chunk) == len(s.chunks) {
			s.chunks = append(s.chunks, make([]Slot, s.chunkSize))
		}
	}

	sl := s.slotPtr(idx)
	if sl.State != StateFree {
		return 0, 0, false
	}
	if sl.Gen == 0 {
		sl.Gen = 1
	}
	s.resetSlot(sl)
	sl.FD = int32(fd)
	sl.State = StateAccepted
	s.live++
	return idx, sl.Gen, true
}

// Get validates (slot, generation) and returns the live slot.
func (s *Slab) Get(slot, gen uint32) (*Slot, bool) {
	if int(slot) >= len(s.chunks)*int(s.chunkSize) || slot >= s.highWater {
		return nil, false
	}
	sl := s.slotPtr(slot)
	if sl.State == StateFree || sl.Gen != gen {
		return nil, false
	}
	return sl, true
}

// GetAny returns a live slot without a generation check. It is for the owning
// loop's internal paths, which already hold a validated handle.
func (s *Slab) GetAny(slot uint32) (*Slot, bool) {
	if int(slot) >= len(s.chunks)*int(s.chunkSize) || slot >= s.highWater {
		return nil, false
	}
	sl := s.slotPtr(slot)
	if sl.State == StateFree {
		return nil, false
	}
	return sl, true
}

// BeginOp records an in-flight operation for a slot. A slot cannot be released
// while any operation is outstanding.
func (s *Slab) BeginOp(slot uint32) bool {
	sl, ok := s.GetAny(slot)
	if !ok {
		return false
	}
	sl.InFlight++
	return true
}

// Release frees a closed slot and bumps its generation. The caller must have
// released every timer node and drained the write queue first.
func (s *Slab) Release(slot uint32) (fd int, ok bool) {
	sl, ok := s.GetAny(slot)
	if !ok {
		return -1, false
	}
	if sl.InFlight != 0 || sl.State != StateClosed {
		return -1, false
	}
	fd = int(sl.FD)
	sl.Gen++
	if sl.Gen == 0 {
		sl.Gen = 1
	}
	sl.State = StateFree
	s.resetSlot(sl)
	s.free = append(s.free, slot)
	s.live--
	return fd, true
}

// transitions is a bitmask of legal destination states for each state.
var transitions [StateClosed + 1]uint16

func init() {
	allow := func(from State, to ...State) {
		for _, t := range to {
			transitions[from] |= 1 << t
		}
	}
	allow(StateFree, StateAccepted)
	allow(StateAccepted, StateHandshaking, StateClosing, StateClosed)
	allow(StateHandshaking, StateAuthenticating, StateOpen, StateClosing, StateClosed)
	allow(StateAuthenticating, StateOpen, StateClosing, StateClosed)
	allow(StateOpen, StateDraining, StateClosing, StateClosed)
	allow(StateDraining, StateClosing, StateClosed)
	allow(StateClosing, StateClosed)
}

// Transition moves a slot to a new state. An illegal transition is counted and
// rejected; the caller treats it as a close.
func (s *Slab) Transition(slot uint32, to State) bool {
	sl, ok := s.GetAny(slot)
	if !ok || to > StateClosed {
		s.illegal++
		return false
	}
	if transitions[sl.State]&(1<<to) == 0 {
		s.illegal++
		return false
	}
	sl.State = to
	return true
}

// Live reports the number of live connections.
func (s *Slab) Live() int { return s.live }

// HighWater reports the highest slot index ever allocated.
func (s *Slab) HighWater() uint32 { return s.highWater }

// SetPeer stores the peer address as a compact tuple.
func (s *Slab) SetPeer(slot uint32, ip []byte, port uint16, family uint8) {
	sl, ok := s.GetAny(slot)
	if !ok {
		return
	}
	sl.Addr = [16]byte{}
	copy(sl.Addr[:], ip)
	sl.AddrLen = uint8(min(len(ip), len(sl.Addr)))
	sl.Port = port
	sl.AddrFamily = family
}

// Peer returns the compact peer tuple.
func (s *Slab) Peer(slot uint32) (ip [16]byte, port uint16, family uint8, ok bool) {
	sl, ok := s.GetAny(slot)
	if !ok || sl.AddrLen == 0 {
		return ip, 0, 0, false
	}
	return sl.Addr, sl.Port, sl.AddrFamily, true
}

// SetTimer records a wheel handle for a deadline kind.
func (s *Slab) SetTimer(slot uint32, kind TimerKind, handle uint32) bool {
	sl, ok := s.GetAny(slot)
	if !ok || kind >= NumTimerKinds {
		return false
	}
	sl.Timers[kind] = handle
	return true
}

// Timer returns the wheel handle for a deadline kind, or NoNode.
func (s *Slab) Timer(slot uint32, kind TimerKind) (uint32, bool) {
	sl, ok := s.GetAny(slot)
	if !ok || kind >= NumTimerKinds {
		return timerwheel.NoNode, false
	}
	return sl.Timers[kind], true
}

// ClearTimer forgets a wheel handle without touching the wheel.
func (s *Slab) ClearTimer(slot uint32, kind TimerKind) {
	if kind >= NumTimerKinds {
		return
	}
	if sl, ok := s.GetAny(slot); ok {
		sl.Timers[kind] = timerwheel.NoNode
	}
}
