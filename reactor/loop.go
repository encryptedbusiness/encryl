//go:build linux

package reactor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	upgrade "github.com/encryptedbusiness/encryl/httpx"
	"github.com/encryptedbusiness/encryl/netio"
	"github.com/encryptedbusiness/encryl/timerwheel"
	"github.com/encryptedbusiness/encryl/uring"
	"github.com/encryptedbusiness/encryl/uring/giouring"
	"github.com/encryptedbusiness/encryl/ws"
	"golang.org/x/sys/unix"
)

const (
	acceptBatch    = 64
	maxCQEsPerPass = 2048
	pollMask       = uint32(unix.POLLIN | unix.POLLERR | unix.POLLHUP | unix.POLLRDHUP)

	// timerAckKind is the wheel node kind for a pending acknowledgement. It
	// does not collide with connection.TimerKind values.
	timerAckKind uint8 = 200
)

type loopCounters struct {
	accepted      atomic.Uint64
	closed        atomic.Uint64
	live          atomic.Int64
	bytesIn       atomic.Uint64
	bytesOut      atomic.Uint64
	messagesIn    atomic.Uint64
	messagesOut   atomic.Uint64
	recvErrors    atomic.Uint64
	sendErrors    atomic.Uint64
	acceptErrors  atomic.Uint64
	fileTableErrs atomic.Uint64
	readPauses    atomic.Uint64
	stale         atomic.Uint64
	cqOverflow    atomic.Uint64
	bufExhausted  atomic.Uint64
	illegal       atomic.Uint64
	eventFDErrors atomic.Uint64
	eventFDErrno  atomic.Int32
	wakeErrors    atomic.Uint64
	timerExpired  atomic.Uint64
	queued        atomic.Int64
	queuedBytes   atomic.Int64
	dropped       atomic.Uint64
	backpressured atomic.Uint64
	evicted       atomic.Uint64
	ackTimeouts   atomic.Uint64
	closeSubs     atomic.Uint64
	closeDone     atomic.Uint64
	closeErrors   atomic.Uint64
	closeErrno    atomic.Int32
	staleSends    atomic.Uint64
	fanoutCmds    atomic.Uint64
	fanoutRefs    atomic.Uint64
	submissions   atomic.Uint64
	completions   atomic.Uint64
}

type loop struct {
	cfg LoopConfig
	ln  netio.Listener

	lnFD       int
	ring       *giouring.Ring
	slab       *connection.Slab
	wheel      *timerwheel.Wheel
	cmd        *CommandRing
	attachRing *AttachRing
	wakeR      int
	wakeW      atomic.Int32
	efdArmed   bool

	rx            *uring.RXArena
	tx            *uring.TXArena
	wsLimits      ws.Limits
	handshakePool []*upgrade.Parser
	nodes         connection.NodePool
	ackTimers     map[uint32]ackTimer

	accepts []acceptDesc
	parked  []uint32

	// retryCancels and retryCloses hold work that could not be submitted
	// because the submission queue was full. The loop retries them before it
	// waits, so a close under submission pressure cannot stall.
	retryCancels []pendingCancel
	retryCloses  []uint32

	start time.Time
	now   uint64

	stop          atomic.Bool
	shuttingDown  bool
	shutdownStart uint64

	timerScratch []uint32
	c            loopCounters
}

var kernelProbe struct {
	once sync.Once
	err  error
}

// requireKernel runs the io_uring feature probe once and fails closed when a
// required opcode is missing. A server that cannot serve must refuse to start.
func requireKernel() error {
	kernelProbe.once.Do(func() {
		if report := uring.Probe(); !report.Ready() {
			kernelProbe.err = fmt.Errorf("reactor: kernel probe failed: %+v", report)
		}
	})
	return kernelProbe.err
}

// NewLoop constructs a loop. It performs no I/O and creates no ring; Run does
// both on the locked OS thread.
func NewLoop(cfg LoopConfig) (Loop, error) {
	if err := requireKernel(); err != nil {
		return nil, err
	}
	if cfg.ReadBufferSize <= 0 {
		cfg.ReadBufferSize = 4096
	}
	if cfg.BufferCount <= 0 {
		cfg.BufferCount = 4096
	}
	if cfg.TxBytes <= 0 {
		cfg.TxBytes = 4 << 20
	}
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = 1_000_000
	}
	if cfg.RingEntries < 64 {
		cfg.RingEntries = 8192
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 2 * time.Minute
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 30 * time.Second
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.MaxHeaderBytes <= 0 {
		cfg.MaxHeaderBytes = 8 << 10
	}
	if cfg.MaxFrameSize <= 0 {
		cfg.MaxFrameSize = 1 << 20
	}
	if cfg.MaxMessageSize <= 0 {
		cfg.MaxMessageSize = 4 << 20
	}
	if cfg.MaxFragments <= 0 {
		cfg.MaxFragments = 128
	}
	if cfg.CloseTimeout <= 0 {
		cfg.CloseTimeout = 5 * time.Second
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 30 * time.Second
	}
	if cfg.WriteQueueMessages <= 0 {
		cfg.WriteQueueMessages = 64
	}
	if cfg.WriteQueueBytes <= 0 {
		cfg.WriteQueueBytes = 256 << 10
	}
	if cfg.MaxPendingAcks <= 0 {
		cfg.MaxPendingAcks = 65536
	}
	var fileLimit unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NOFILE, &fileLimit) == nil &&
		fileLimit.Max > 0 && uint64(cfg.MaxConnections) > fileLimit.Max {
		cfg.MaxConnections = int(fileLimit.Max)
	}

	rx, err := uring.NewRXArena(cfg.ReadBufferSize, cfg.BufferCount)
	if err != nil {
		return nil, err
	}
	tx, err := uring.NewTXArena(cfg.TxBytes, cfg.ReadBufferSize)
	if err != nil {
		rx.Close()
		return nil, err
	}

	maxNodes := cfg.MaxConnections*int(connection.NumTimerKinds) + cfg.MaxPendingAcks + 1024
	l := &loop{
		cfg: cfg,
		wsLimits: ws.Limits{
			MaxFrameSize:   cfg.MaxFrameSize,
			MaxMessageSize: cfg.MaxMessageSize,
			MaxFragments:   cfg.MaxFragments,
		},
		ln:           cfg.Listener,
		slab:         connection.NewSlab(cfg.MaxConnections, 512),
		wheel:        timerwheel.New(0, maxNodes),
		cmd:          NewCommandRing(4096),
		attachRing:   NewAttachRing(16384),
		rx:           rx,
		tx:           tx,
		accepts:      make([]acceptDesc, acceptBatch),
		parked:       make([]uint32, 0, 64),
		timerScratch: make([]uint32, 0, 64),
		ackTimers:    make(map[uint32]ackTimer),
	}
	l.wakeW.Store(-1)
	return l, nil
}

func (l *loop) logf(format string, args ...any) {
	if l.cfg.Logf != nil {
		l.cfg.Logf(format, args...)
	}
}

func (l *loop) Index() int { return l.cfg.Index }

func (l *loop) Addr() net.Addr {
	if l.ln == nil {
		return nil
	}
	return l.ln.Addr()
}

func (l *loop) Enqueue(c Command) bool { return l.cmd.Push(c) }

// SendLocal is the loop-thread-only fast path used by event callbacks. It
// avoids the command ring mutex and the eventfd wake; callers must already be
// on this loop. The frame reference is consumed.
func (l *loop) SendLocal(slot, gen uint32, frame *connection.Frame) bool {
	if l == nil || frame == nil {
		return false
	}
	return l.enqueueOwned(slot, gen, frame)
}

func (l *loop) EnqueueAttach(c AttachCommand) bool { return l.attachRing.Push(c) }

func (l *loop) ConnMeta(slot, gen uint32) (ConnMeta, bool) {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		return ConnMeta{}, false
	}
	return ConnMeta{
		Acct:     sl.Acct(),
		IP:       sl.Addr,
		Port:     sl.Port,
		Family:   sl.AddrFamily,
		Encoding: sl.Encoding,
	}, true
}

func (l *loop) EnsureAccounting(slot, gen uint32) *connection.Accounting {
	sl, ok := l.slab.Get(slot, gen)
	if !ok {
		return nil
	}
	if a := sl.Acct(); a != nil {
		return a
	}
	a := &connection.Accounting{}
	sl.SetAcct(a)
	return a
}

// CancelAck cancels a pending-acknowledgement timeout. Loop-thread-only.
func (l *loop) CancelAck(ackSlot uint32) { l.cancelAckTimer(ackSlot) }

func (l *loop) acquireHandshake() *upgrade.Parser {
	if n := len(l.handshakePool); n > 0 {
		p := l.handshakePool[n-1]
		l.handshakePool = l.handshakePool[:n-1]
		p.Reset()
		return p
	}
	return upgrade.NewParser(upgrade.Limits{
		MaxHeaderBytes: l.cfg.MaxHeaderBytes,
		MaxHeaders:     100,
	})
}

func (l *loop) releaseHandshake(sl *connection.Slot) {
	p := sl.Handshake()
	if p == nil {
		return
	}
	sl.SetHandshake(nil)
	p.Reset()
	if len(l.handshakePool) < 256 {
		l.handshakePool = append(l.handshakePool, p)
	}
}

func (l *loop) Shutdown() error {
	l.stop.Store(true)
	l.wake()
	return nil
}

func (l *loop) QueueLen() int { return l.cmd.Len() + l.attachRing.Len() }

// Run owns the calling goroutine until ctx is cancelled or Shutdown is called.
// ringFallbackSizes are tried, in order, when the requested ring cannot be
// created. Ring size is a throughput tuning knob; a smaller ring still serves
// correctly on kernels or sandboxes that reject large queues.
var ringFallbackSizes = []uint32{8192, 4096, 1024, 256}

// ringSizes returns the requested entries followed by strictly smaller
// fallbacks. The list is deterministic and used by openRing.
func ringSizes(entries uint32) []uint32 {
	out := make([]uint32, 0, len(ringFallbackSizes)+1)
	out = append(out, entries)
	for _, n := range ringFallbackSizes {
		if n < entries {
			out = append(out, n)
		}
	}
	return out
}

// openRing creates the largest ring this kernel accepts, falling back to
// smaller queues when a large one is rejected (EINVAL/ENOMEM/EPERM).
func openRing(entries uint32, logf func(string, ...any)) (*giouring.Ring, error) {
	var firstErr error
	for i, n := range ringSizes(entries) {
		ring, err := uring.NewRing(n)
		if err == nil {
			if i > 0 && logf != nil {
				logf("reactor: ring size %d unavailable (%v); using %d", entries, firstErr, n)
			}
			return ring, nil
		}
		if i == 0 {
			firstErr = err
		}
	}
	return nil, firstErr
}

func (l *loop) Run(ctx context.Context) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	l.lnFD = -1
	if l.ln != nil {
		l.lnFD = l.ln.FD()
		if l.lnFD < 0 {
			return errors.New("reactor: listener is closed")
		}
	}

	ring, err := openRing(l.cfg.RingEntries, l.cfg.Logf)
	if err != nil {
		return err
	}
	l.ring = ring
	if _, err := ring.RegisterFilesSparse(uint32(l.cfg.MaxConnections)); err != nil {
		ring.QueueExit()
		l.ring = nil
		return fmt.Errorf("reactor: register file table: %w", err)
	}

	efd, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		ring.QueueExit()
		l.ring = nil
		return fmt.Errorf("reactor: eventfd: %w", err)
	}
	l.wakeR = efd
	l.wakeW.Store(int32(efd))

	defer l.dropAttach()
	defer func() {
		ring.QueueExit()
		l.ring = nil
		unix.Close(efd)
		l.wakeW.Store(-1)
		l.rx.Close()
		l.tx.Close()
	}()

	l.cmd.SetWake(l.wake)
	l.attachRing.SetWake(l.wake)
	l.start = time.Now()
	l.now = 0

	l.armAccepts()
	l.armEventFD()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = l.Shutdown()
		case <-done:
		}
	}()

	lastCQEs := 0
	for {
		l.now = uint64(time.Since(l.start).Milliseconds())

		if l.stop.Load() {
			l.progressShutdown()
			if l.c.live.Load() == 0 {
				return nil
			}
		}

		l.drainAttach()
		l.drainCommands()
		l.armAccepts()
		l.wakeParked()
		if !l.efdArmed {
			l.armEventFD()
		}
		// Completions first: an acknowledgement that lands in its deadline
		// tick must complete, not be reported as a timeout by a timer that
		// advances before the completion is seen.
		lastCQEs = l.processCQEs(maxCQEsPerPass)
		l.retryPendingOps()
		l.advanceTimers()

		waitNr := uint32(1)
		waitNS := int64(timerwheel.TickMillis) * int64(time.Millisecond)
		if lastCQEs >= 4 {
			waitNr = 32
			waitNS = int64(250 * time.Microsecond)
		}
		if l.stop.Load() {
			waitNr = 1
			waitNS = int64(time.Millisecond)
		}
		waitTS := syscall.NsecToTimespec(waitNS)
		if _, err := ring.SubmitAndWaitTimeout(waitNr, &waitTS, nil); err != nil {
			if errors.Is(err, unix.ETIME) || errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("reactor: submit: %w", err)
		}
	}
}

func (l *loop) wake() {
	fd := l.wakeW.Load()
	if fd < 0 {
		return
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], 1)
	if _, err := unix.Write(int(fd), b[:]); err != nil && err != unix.EAGAIN {
		l.c.wakeErrors.Add(1)
	}
}

func (l *loop) getSQE() *giouring.SubmissionQueueEntry {
	if l.ring == nil {
		return nil
	}
	if sqe := l.ring.GetSQE(); sqe != nil {
		return sqe
	}
	_, _ = l.ring.Submit()
	return l.ring.GetSQE()
}

func (l *loop) processCQEs(budget int) int {
	n := 0
	for i := 0; i < budget; i++ {
		if l.ring.CQReady() == 0 {
			break
		}
		cqe, err := l.ring.PeekCQE()
		if err != nil {
			break
		}
		l.c.completions.Add(1)
		l.handle(cqe)
		l.ring.CQESeen(cqe)
		n++
	}
	if l.ring.CQHasOverflow() {
		l.c.cqOverflow.Add(1)
		_, _ = l.ring.GetEvents()
	}
	return n
}

func (l *loop) handle(cqe *giouring.CompletionQueueEvent) {
	op, slot, gen, ok := DecodeUserData(cqe.UserData)
	if !ok {
		l.c.stale.Add(1)
		return
	}
	switch op {
	case OpAccept:
		l.handleAccept(cqe, slot)
	case OpPoll:
		l.handlePoll(cqe, slot, gen)
	case OpRecv:
		l.handleRecv(cqe, slot, gen)
	case OpSend:
		l.handleSend(cqe, slot, gen)
	case OpClose:
		l.handleClose(cqe, slot, gen)
	case OpEventFD:
		l.handleEventFD(cqe)
	case OpCancel:
	default:
		l.c.stale.Add(1)
	}
}

func (l *loop) armEventFD() {
	sqe := l.getSQE()
	if sqe == nil {
		return
	}
	sqe.PreparePollAdd(l.wakeR, unix.POLLIN)
	sqe.UserData = EncodeUserData(OpEventFD, 0, 0)
	l.efdArmed = true
	l.c.submissions.Add(1)
}

func (l *loop) handleEventFD(cqe *giouring.CompletionQueueEvent) {
	l.efdArmed = false
	if cqe.Res < 0 && cqe.Res != -int32(unix.EAGAIN) && cqe.Res != -int32(unix.ECANCELED) {
		l.c.eventFDErrno.CompareAndSwap(0, -cqe.Res)
		l.c.eventFDErrors.Add(1)
	} else {
		var b [8]byte
		for {
			if _, err := unix.Read(l.wakeR, b[:]); err != nil {
				break
			}
		}
	}
	l.drainAttach()
	l.drainCommands()
	if !l.stop.Load() {
		l.armEventFD()
	}
}
