//go:build linux

// Package reactor is the shared kernel engine: one pinned OS thread, one
// io_uring ring, and one fixed set of connections per loop. It drains
// completions and drives the connection slice through callbacks.
package reactor

import (
	"context"
	"net"
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/netio"
)

// Listener is a raw listening socket owned by the reactor.
type Listener = netio.Listener

// LoopConfig configures one event loop. A loop owns one ring and, in owned
// mode, one listener. A nil Listener puts the loop in attached mode.
type LoopConfig struct {
	Index    int
	Listener netio.Listener

	ReadBufferSize int
	BufferCount    int
	TxBytes        int64
	MaxConnections int
	// RingEntries is the preferred submission queue depth. If the kernel
	// rejects a ring of this size, Run retries with a smaller queue.
	RingEntries uint32

	// WriteQueueMessages and WriteQueueBytes bound each connection's outbound
	// queue. Backpressure selects the full-queue behaviour.
	WriteQueueMessages int
	WriteQueueBytes    int64
	Backpressure       connection.BackpressurePolicy
	MaxPendingAcks     int

	HandshakeTimeout time.Duration
	IdleTimeout      time.Duration
	PingInterval     time.Duration
	CloseTimeout     time.Duration
	ShutdownTimeout  time.Duration

	MaxHeaderBytes int
	MaxFrameSize   int64
	MaxMessageSize int64
	MaxFragments   int
	AllowedOrigins []string

	// EchoMessages is a test mode that echoes every complete binary message.
	EchoMessages bool

	// OnMessage runs for each complete binary message. The payload is borrowed.
	OnMessage func(slot, gen uint32, payload []byte)

	// RawEcho is a test-only mode. It echoes received bytes without parsing a
	// WebSocket upgrade or frame. It is never set by the public server.
	RawEcho bool

	// OnOpen runs after a connection is accepted and before any data is read.
	// The peer address is passed inline so the reactor does not retain a
	// per-connection net.Addr. acct is the connection's admission gate.
	OnOpen func(slot, gen uint32, acct *connection.Accounting, ip [16]byte, port uint16, family uint8, encoding uint8)

	// ConnMeta returns the data needed to create a public Conn without
	// keeping a Conn for every idle socket.
	ConnMeta func(slot, gen uint32) (ConnMeta, bool)

	// OnClose runs once when a connection is closed. remote reports that the
	// peer sent the close frame or shut the stream down first.
	OnClose func(slot, gen uint32, code uint16, reason string, remote bool)

	// OnAckTimeout runs on the owning loop when a pending acknowledgement
	// timer expires. ackSlot is the slab index from event.Pending and gen is
	// the generation the timer was armed with; the slab rejects a mismatch.
	OnAckTimeout func(ackSlot, gen uint32)

	// Logf is an optional debug hook.
	Logf func(format string, args ...any)
}

// LoopStats is a point-in-time snapshot from one loop.
type LoopStats struct {
	Index int

	Accepted uint64
	Closed   uint64
	Live     int

	BytesIn     uint64
	BytesOut    uint64
	MessagesIn  uint64
	MessagesOut uint64

	RecvErrors         uint64
	SendErrors         uint64
	AcceptErrors       uint64
	FileTableErrors    uint64
	ReadPauses         uint64
	StaleCompletions   uint64
	CQOverflow         uint64
	BufferExhausted    uint64
	IllegalTransitions uint64
	EventFDErrors      uint64
	EventFDErrno       int32
	WakeErrors         uint64
	TimerExpired       uint64

	Queued        int64
	QueuedBytes   int64
	Dropped       uint64
	Backpressured uint64
	Evicted       uint64
	AckTimeouts   uint64

	StaleSends     uint64
	CloseSubmitted uint64
	CloseCompleted uint64
	CloseErrors    uint64
	CloseErrno     int32
	FanoutCommands uint64
	FanoutRefs     uint64

	Submissions uint64
	Completions uint64
}

// ConnMeta is the compact state needed to build a public Conn after a socket
// has been idle.
type ConnMeta struct {
	Acct     *connection.Accounting
	IP       [16]byte
	Port     uint16
	Family   uint8
	Encoding uint8
}

// Loop is one single-owner reactor.
type Loop interface {
	Index() int
	Addr() net.Addr
	Run(ctx context.Context) error
	Enqueue(Command) bool
	EnqueueAttach(AttachCommand) bool

	// SendLocal hands one frame directly to the owning loop's send queue. It
	// is loop-thread-only: call it from OnMessage/OnID/OnOpen callbacks, never
	// from another goroutine. It consumes the frame reference.
	SendLocal(slot, gen uint32, frame *connection.Frame) bool
	Shutdown() error
	Stats() LoopStats
	QueueLen() int

	// ConnMeta reads connection metadata on the owning loop.
	ConnMeta(slot, gen uint32) (ConnMeta, bool)

	// EnsureAccounting creates the admission gate only when a connection first
	// needs a public handle.
	EnsureAccounting(slot, gen uint32) *connection.Accounting

	// CancelAck cancels a pending-acknowledgement timeout. It is
	// loop-thread-only.
	CancelAck(ackSlot uint32)
}
