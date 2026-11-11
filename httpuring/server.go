//go:build linux

package httpuring

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/encryptedbusiness/encryl/netio"
	"github.com/encryptedbusiness/encryl/timerwheel"
	"github.com/encryptedbusiness/encryl/uring"
	"github.com/encryptedbusiness/encryl/uring/giouring"
	"golang.org/x/sys/unix"
)

const (
	rawOpAccept uint8 = iota + 1
	rawOpPoll
	rawOpRecv
	rawOpSend
	rawOpWake
)

const (
	fastInSize     = 8 << 10
	fastArenaCount = 1024
	httpDateFmt    = "Mon, 02 Jan 2006 15:04:05 GMT"
	noBid          = int32(-1)
	slotChunk      = 256
)

const (
	flagLive uint16 = 1 << iota
	flagPoll
	flagRecv
	flagSend
	flagCloseAfter
	flagHTTPPending
	flagHijacked
	flagParkedRecv
	flagParkedSend
)

const (
	kindHeader uint8 = iota
	kindIdle
	kindWrite
	kindCount
)

// maxSlots bounds the slot table to the 24 bits reserved in user_data.
const maxSlots = 1 << 24

// Cached header-parse state for one connection. parse handles an incremental
// request stream, so the terminator scan and the header lines are scanned once
// per request instead of once per receive.
const (
	hdrParsed uint8 = 1 << iota
	hdrClose
	hdrKeepAlive
	hdrExpectContinue
	hdrContinueSent
)

func packUser(op uint8, slot, gen uint32) uint64 {
	return uint64(op)<<56 | uint64(gen)<<24 | uint64(slot)&((1<<24)-1)
}

func unpackUser(u uint64) (op uint8, slot, gen uint32) {
	return uint8(u >> 56), uint32(u & ((1 << 24) - 1)), uint32(u >> 24)
}

// Request is a parsed HTTP/1.x request. Slices are borrowed from the
// connection's receive buffer and are valid only during ServeHTTP.
type Request struct {
	Method []byte
	Path   []byte
	Proto  []byte
	Header []byte
	Body   []byte
}

// Response is a raw HTTP response. The server supplies status, Server, Date,
// Content-Length, and Connection headers.
type Response struct {
	Status      int
	ContentType []byte
	Body        []byte
	Close       bool
}

// Handler serves one parsed request.
type Handler interface {
	ServeHTTP(*Request, *Response)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(*Request, *Response)

func (f HandlerFunc) ServeHTTP(req *Request, res *Response) { f(req, res) }

type route struct {
	path []byte
	h    Handler
}

// Mux is a small exact-path router. It performs no allocations on lookup.
type Mux struct {
	routes []route
}

// Handle registers an exact path.
func (m *Mux) Handle(path string, h Handler) {
	m.routes = append(m.routes, route{path: []byte(path), h: h})
}

// HandleFunc registers an exact path.
func (m *Mux) HandleFunc(path string, h func(*Request, *Response)) {
	m.Handle(path, HandlerFunc(h))
}

func (m *Mux) ServeHTTP(req *Request, res *Response) {
	for i := range m.routes {
		if bytes.Equal(req.Path, m.routes[i].path) {
			m.routes[i].h.ServeHTTP(req, res)
			return
		}
	}
	res.Status = 404
}

// Server is a single-loop raw HTTP/1.1 server on io_uring.
type Server struct {
	Addr           string
	Handler        Handler      // raw exact-path fast path
	HTTPHandler    http.Handler // chi, Echo, Gin, or http.ServeMux
	Workers        int          // 0: inline HTTPHandler; >0: request/response ring
	Loops          int          // SO_REUSEPORT loops; keep <= vCPUs
	MaxConnections int          // 0: unlimited; applies before hijack
	// Buffers bounds the per-loop receive buffers and outbound blocks.
	// 0 uses the default (1024). It is clamped to MaxConnections when set.
	// Each buffer costs fastInSize bytes, so the fixed arena cost per loop is
	// roughly 2*Buffers*fastInSize regardless of how many connections are idle.
	Buffers       int
	HeaderTimeout time.Duration
	IdleTimeout   time.Duration
	WriteTimeout  time.Duration
	// MaxOutputBytes bounds pipelined and queued output per connection. A
	// single response larger than this uses a private heap block; while a
	// response is in flight, further request parsing pauses until it drains.
	MaxOutputBytes int
	ServerHeader   string

	// OnRequest is called on the ring loop after a response has been fully
	// written. It is optional and intended for benchmarking/tracing.
	OnRequest func(time.Duration)

	defaultsOnce sync.Once
	mu           sync.Mutex
	loops        []*fastLoop
	listens      []netio.Listener
}

// applyDefaults initializes zero-valued limits exactly once, so concurrent
// Serve calls for a multi-loop server never race on the shared config.
func (s *Server) applyDefaults() {
	s.defaultsOnce.Do(func() {
		if s.HeaderTimeout <= 0 {
			s.HeaderTimeout = 10 * time.Second
		}
		if s.IdleTimeout <= 0 {
			s.IdleTimeout = 2 * time.Minute
		}
		if s.WriteTimeout <= 0 {
			s.WriteTimeout = 30 * time.Second
		}
		if s.MaxOutputBytes <= 0 {
			s.MaxOutputBytes = 256 << 10
		}
	})
}

// ListenAndServe binds Addr and runs the loop.
func (s *Server) ListenAndServe() error {
	loops := s.Loops
	if loops < 1 {
		loops = 1
	}
	if loops == 1 {
		ln, err := netio.Listen(s.Addr, false, 65535)
		if err != nil {
			return err
		}
		return s.Serve(ln)
	}
	lns := make([]netio.Listener, 0, loops)
	for i := 0; i < loops; i++ {
		ln, err := netio.Listen(s.Addr, true, 65535)
		if err != nil {
			for _, prev := range lns {
				_ = prev.Close()
			}
			return err
		}
		lns = append(lns, ln)
	}
	errCh := make(chan error, loops)
	for _, ln := range lns {
		go func(ln netio.Listener) { errCh <- s.Serve(ln) }(ln)
	}
	return <-errCh
}

// Serve runs the loop on an existing listener. It blocks until the listener
// fails or Close is called.
func (s *Server) Serve(ln netio.Listener) error {
	if s.Handler == nil && s.HTTPHandler == nil {
		return fmt.Errorf("httpuring: nil handler")
	}
	s.applyDefaults()
	buffers := s.Buffers
	if buffers <= 0 {
		buffers = fastArenaCount
	}
	if s.MaxConnections > 0 && s.MaxConnections < buffers {
		buffers = s.MaxConnections
	}
	if buffers < 1 {
		buffers = 1
	}
	rx, err := uring.NewRXArena(fastInSize, buffers)
	if err != nil {
		return err
	}
	tx, err := uring.NewTXArena(int64(fastInSize)*int64(buffers), fastInSize)
	if err != nil {
		_ = rx.Close()
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ring, err := uring.NewRing(4096)
	if err != nil {
		_ = rx.Close()
		_ = tx.Close()
		return err
	}
	defer ring.QueueExit()

	f := &fastLoop{
		server: s,
		ln:     ln,
		lnFD:   ln.FD(),
		ring:   ring,
		rx:     rx,
		tx:     tx,
		wheel:  timerwheel.New(0, 0),
		start:  time.Now(),
	}
	defer f.closeAll()
	if s.HTTPHandler != nil {
		f.jobs = newHTTPJobPool()
		if s.Workers > 0 {
			f.reqCh = make(chan *httpJob, 1024)
			f.respCh = make(chan *httpJob, 1024)
			f.stopCh = make(chan struct{})
		}
	}
	wakeFD, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		return err
	}
	f.wakeFD = wakeFD
	defer func() { _ = unix.Close(wakeFD) }()
	if f.stopCh != nil {
		for i := 0; i < s.Workers; i++ {
			f.workers.Add(1)
			go f.worker()
		}
		// Registered after the wakeFD close so it runs first (LIFO): join the
		// workers while wakeFD is still valid, and before f.closeAll unmaps the
		// arenas an in-flight handler may still be reading its body from.
		defer func() {
			close(f.stopCh)
			f.workers.Wait()
		}()
	}
	s.mu.Lock()
	s.loops = append(s.loops, f)
	s.listens = append(s.listens, ln)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		for i, l := range s.loops {
			if l == f {
				s.loops = append(s.loops[:i], s.loops[i+1:]...)
				break
			}
		}
		for i, l := range s.listens {
			if l == ln {
				s.listens = append(s.listens[:i], s.listens[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}()
	if !f.armWake() {
		return fmt.Errorf("httpuring: wake submission failed")
	}
	f.submitAccept()
	waitTS := syscall.NsecToTimespec(int64(timerwheel.TickMillis) * int64(time.Millisecond))
	for {
		if f.stop.Load() {
			return nil
		}
		f.wakeParkedRecv()
		f.wakeParkedSend()
		f.advanceTimers()
		if !f.stop.Load() && f.acceptPending == 0 && f.now >= f.acceptRetryAt {
			// Recover accepts after a resource-exhaustion error once the
			// backoff deadline passes; never retry in a hot loop.
			f.submitAccept()
		}
		if _, err := ring.SubmitAndWaitTimeout(1, &waitTS, nil); err != nil {
			if errors.Is(err, unix.ETIME) || errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		f.processCQEs()
	}
}

type fastLoop struct {
	server *Server
	ln     netio.Listener
	lnFD   int
	ring   *giouring.Ring
	rx     *uring.RXArena
	tx     *uring.TXArena
	slots  slotTable

	acceptPending int
	acceptRetryAt uint64
	acceptBackoff uint64
	ringBroken    bool
	stop          atomic.Bool
	wakeFD        int
	wakeArmed     bool
	dateUnix      int64
	date          []byte
	dateCount     uint32
	build         []byte
	req           Request
	res           Response
	respBody      []byte

	wheel      *timerwheel.Wheel
	start      time.Time
	now        uint64
	fired      []uint32
	parkedRecv []uint32
	parkedSend []uint32

	parkedRecvBusy bool
	parkedSendBusy bool

	jobs    *httpJobPool
	reqCh   chan *httpJob
	respCh  chan *httpJob
	stopCh  chan struct{}
	workers sync.WaitGroup
}

type httpExtra struct {
	job          *httpJob
	httpConsumed int
	pending      []byte
}

type fastConn struct {
	fd         int32
	id         uint32
	gen        uint32
	flags      uint16
	addrLen    uint8
	addrFamily uint8
	port       uint16
	rxBid      int32
	txBid      int32
	rxStart    int32
	rxEnd      int32
	txOff      int32
	txLen      int32
	scanOff    int32
	hdrEnd     int32
	hdrLen     int32
	hdrBits    uint8
	reqStart   int64
	txHeap     []byte
	addr       [16]byte
	timers     [kindCount]uint32
	extra      *httpExtra
}

func (c *fastConn) has(b uint16) bool { return c.flags&b != 0 }
func (c *fastConn) set(b uint16)      { c.flags |= b }
func (c *fastConn) clear(b uint16)    { c.flags &^= b }

type slotTable struct {
	chunks [][]fastConn
	free   []uint32
	next   uint32
	live   int
}

func (t *slotTable) get(id uint32) *fastConn {
	return &t.chunks[id/slotChunk][id%slotChunk]
}

func (t *slotTable) alloc(fd int) (uint32, *fastConn) {
	var id uint32
	if n := len(t.free); n > 0 {
		id = t.free[n-1]
		t.free = t.free[:n-1]
	} else {
		id = t.next
		t.next++
		for uint32(len(t.chunks))*slotChunk <= id {
			ch := make([]fastConn, slotChunk)
			for i := range ch {
				ch[i].fd = -1
				ch[i].rxBid = noBid
				ch[i].txBid = noBid
				ch[i].hdrEnd = -1
				for k := range ch[i].timers {
					ch[i].timers[k] = timerwheel.NoNode
				}
			}
			t.chunks = append(t.chunks, ch)
		}
	}
	c := t.get(id)
	gen := c.gen
	if gen == 0 {
		gen = 1
	}
	*c = fastConn{
		fd:     int32(fd),
		id:     id,
		gen:    gen,
		flags:  flagLive,
		rxBid:  noBid,
		txBid:  noBid,
		hdrEnd: -1,
	}
	for k := range c.timers {
		c.timers[k] = timerwheel.NoNode
	}
	t.live++
	return id, c
}

func (t *slotTable) release(id uint32) {
	c := t.get(id)
	gen := c.gen + 1
	if gen == 0 {
		gen = 1
	}
	*c = fastConn{id: id, gen: gen, fd: -1, rxBid: noBid, txBid: noBid, hdrEnd: -1}
	for k := range c.timers {
		c.timers[k] = timerwheel.NoNode
	}
	t.live--
	t.free = append(t.free, id)
	if t.live == 0 {
		// Drop the high-water slab once the loop is empty so a burst of
		// short-lived connections does not pin memory for the loop's lifetime.
		t.chunks = nil
		t.free = nil
		t.next = 0
	}
}

func (f *fastLoop) slotFor(slot, gen uint32) *fastConn {
	if slot >= f.slots.next {
		return nil
	}
	c := f.slots.get(slot)
	if c.gen != gen {
		return nil
	}
	return c
}

// Close stops the loop and closes the listening socket.
func (s *Server) Close() error {
	s.mu.Lock()
	loops := append([]*fastLoop(nil), s.loops...)
	listens := append([]netio.Listener(nil), s.listens...)
	s.mu.Unlock()
	var first error
	for _, f := range loops {
		f.stop.Store(true)
		f.wake()
	}
	for _, ln := range listens {
		if err := ln.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (f *fastLoop) wake() {
	if f.wakeFD < 0 {
		return
	}
	var b [8]byte
	*(*uint64)(unsafe.Pointer(&b[0])) = 1
	_, _ = unix.Write(f.wakeFD, b[:])
}

func (f *fastLoop) armWake() bool {
	if f.wakeArmed {
		return true
	}
	sqe := f.getSQE()
	if sqe == nil {
		return false
	}
	sqe.PreparePollAdd(f.wakeFD, unix.POLLIN)
	sqe.UserData = packUser(rawOpWake, 0, 0)
	f.wakeArmed = true
	return true
}

func (f *fastLoop) getSQE() *giouring.SubmissionQueueEntry {
	if f.ring == nil {
		return nil
	}
	if sqe := f.ring.GetSQE(); sqe != nil {
		return sqe
	}
	if _, err := f.ring.Submit(); err != nil {
		return nil
	}
	return f.ring.GetSQE()
}

const fastAcceptBatch = 64

// handleAcceptError keeps transient accept failures from tearing down the loop.
func (f *fastLoop) handleAcceptError(err unix.Errno) {
	switch err {
	case unix.ECONNABORTED, unix.EINTR, unix.EAGAIN:
		// The pending connection vanished; accept the next one right away.
		f.acceptRetryAt = 0
		f.submitAccept()
	case unix.EMFILE, unix.ENFILE, unix.ENOBUFS, unix.ENOMEM, unix.EPERM,
		// Linux passes already-pending network errors from the new socket
		// through accept(). Retry them with backoff instead of stopping the
		// loop or spinning while the condition persists.
		unix.ECONNRESET, unix.EPROTO, unix.ENETDOWN, unix.ENETUNREACH,
		unix.EHOSTDOWN, unix.EHOSTUNREACH, unix.ENONET, unix.ENOPROTOOPT,
		unix.EOPNOTSUPP, unix.ETIMEDOUT, unix.ENOSR, unix.ESOCKTNOSUPPORT,
		unix.EPROTONOSUPPORT:
		// Descriptor, memory, or network exhaustion. Back off; the main
		// loop re-arms accepts once the retry deadline passes.
		now := f.now
		if now == 0 {
			now = uint64(time.Since(f.start).Milliseconds())
		}
		backoff := f.acceptBackoff
		switch {
		case backoff == 0:
			backoff = 100
		case backoff < 1000:
			backoff *= 2
			if backoff > 1000 {
				backoff = 1000
			}
		}
		f.acceptBackoff = backoff
		f.acceptRetryAt = now + backoff
	default:
		f.stop.Store(true)
	}
}

func (f *fastLoop) submitAccept() {
	for f.acceptPending < fastAcceptBatch && !f.stop.Load() {
		sqe := f.getSQE()
		if sqe == nil {
			return
		}
		sqe.PrepareAccept(f.lnFD, 0, 0, unix.SOCK_CLOEXEC)
		sqe.UserData = packUser(rawOpAccept, 0, 0)
		f.acceptPending++
	}
}

func (f *fastLoop) processCQEs() {
	for f.ring.CQReady() > 0 {
		cqe, err := f.ring.PeekCQE()
		if err != nil {
			return
		}
		f.handle(cqe)
		f.ring.CQESeen(cqe)
	}
}

func (f *fastLoop) handle(cqe *giouring.CompletionQueueEvent) {
	op, slot, gen := unpackUser(cqe.UserData)
	switch op {
	case rawOpAccept:
		if f.acceptPending > 0 {
			f.acceptPending--
		}
		if cqe.Res < 0 {
			f.handleAcceptError(unix.Errno(-cqe.Res))
			return
		}
		fd := int(cqe.Res)
		if f.stop.Load() {
			_ = unix.Close(fd)
			return
		}
		if f.server.MaxConnections > 0 && f.slots.live >= f.server.MaxConnections {
			_ = unix.Close(fd)
			f.submitAccept()
			return
		}
		if f.slots.next >= maxSlots {
			// user_data reserves 24 bits for the slot; never wrap it.
			_ = unix.Close(fd)
			f.submitAccept()
			return
		}
		_ = netio.TuneAcceptedFD(fd)
		_ = unix.SetNonblock(fd, true)
		_, c := f.slots.alloc(fd)
		f.acceptBackoff = 0
		f.acceptRetryAt = 0
		storePeer(c, fd)
		f.submitAccept()
		f.armTimer(c, kindHeader, f.server.HeaderTimeout)
		f.submitPoll(c)
	case rawOpPoll:
		f.handlePoll(slot, gen, cqe.Res)
	case rawOpRecv:
		f.handleRecv(slot, gen, cqe.Res)
	case rawOpSend:
		f.handleSend(slot, gen, cqe.Res)
	case rawOpWake:
		f.wakeArmed = false
		var b [8]byte
		for {
			if _, err := unix.Read(f.wakeFD, b[:]); err != nil {
				break
			}
		}
		f.drainResponses()
		if !f.stop.Load() && !f.armWake() {
			f.stop.Store(true)
		}
	}
}

func storePeer(c *fastConn, fd int) {
	sa, err := unix.Getpeername(fd)
	if err != nil {
		return
	}
	switch s := sa.(type) {
	case *unix.SockaddrInet4:
		copy(c.addr[:], s.Addr[:])
		c.addrLen = 4
		c.port = uint16(s.Port)
		c.addrFamily = unix.AF_INET
	case *unix.SockaddrInet6:
		copy(c.addr[:], s.Addr[:])
		c.addrLen = 16
		c.port = uint16(s.Port)
		c.addrFamily = unix.AF_INET6
	}
}

func peerString(c *fastConn) string {
	if c == nil || c.addrLen == 0 {
		return ""
	}
	ip := net.IP(c.addr[:c.addrLen])
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(c.port)))
}

func (f *fastLoop) advanceTimers() {
	if f.wheel == nil {
		return
	}
	f.now = uint64(time.Since(f.start).Milliseconds())
	f.fired = f.wheel.Advance(f.now, f.fired[:0])
	for _, h := range f.fired {
		n := f.wheel.Node(h)
		if n == nil {
			continue
		}
		id := n.Owner
		kind := n.Kind
		f.wheel.Release(h)
		if id >= f.slots.next {
			continue
		}
		c := f.slots.get(id)
		if !c.has(flagLive) || int(kind) >= len(c.timers) || c.timers[kind] != h {
			// The node no longer belongs to this generation of the slot.
			continue
		}
		c.timers[kind] = timerwheel.NoNode
		f.closeConn(c)
	}
}

func (f *fastLoop) armTimer(c *fastConn, kind uint8, d time.Duration) {
	if f.wheel == nil || c == nil || d <= 0 || int(kind) >= len(c.timers) {
		return
	}
	h := c.timers[kind]
	if h == timerwheel.NoNode {
		var ok bool
		h, ok = f.wheel.Acquire()
		if !ok {
			return
		}
		f.wheel.SetMeta(h, c.id, kind)
		c.timers[kind] = h
	}
	now := f.now
	if now == 0 {
		now = uint64(time.Since(f.start).Milliseconds())
		f.now = now
	}
	f.wheel.Add(h, now, now+uint64(d/time.Millisecond))
}

func (f *fastLoop) disarmTimer(c *fastConn, kind uint8) {
	if f.wheel == nil || c == nil || int(kind) >= len(c.timers) {
		return
	}
	h := c.timers[kind]
	if h == timerwheel.NoNode {
		return
	}
	f.wheel.Release(h)
	c.timers[kind] = timerwheel.NoNode
}

func (f *fastLoop) disarmAll(c *fastConn) {
	for kind := uint8(0); kind < kindCount; kind++ {
		f.disarmTimer(c, kind)
	}
}

func (f *fastLoop) submitPoll(c *fastConn) {
	if c == nil || !c.has(flagLive) || c.has(flagPoll|flagRecv|flagCloseAfter|flagHTTPPending|flagHijacked|flagParkedRecv) {
		return
	}
	sqe := f.getSQE()
	if sqe == nil {
		f.closeConn(c)
		return
	}
	sqe.PreparePollAdd(int(c.fd), unix.POLLIN)
	sqe.UserData = packUser(rawOpPoll, c.id, c.gen)
	c.set(flagPoll)
}

func (f *fastLoop) handlePoll(slot, gen uint32, res int32) {
	c := f.slotFor(slot, gen)
	if c == nil {
		return
	}
	c.clear(flagPoll)
	if !c.has(flagLive) {
		f.tryRelease(c, gen)
		return
	}
	if res < 0 {
		if res == -int32(unix.ECANCELED) {
			return
		}
		f.closeConn(c)
		return
	}
	if res&int32(unix.POLLIN) != 0 {
		f.submitRecv(c)
		return
	}
	if res&int32(unix.POLLERR|unix.POLLHUP) != 0 {
		f.closeConn(c)
		return
	}
	f.submitPoll(c)
}

func (f *fastLoop) submitRecv(c *fastConn) {
	if c == nil || !c.has(flagLive) || c.has(flagRecv|flagCloseAfter|flagHTTPPending|flagHijacked) {
		return
	}
	if int(c.txLen) >= f.maxOut() {
		return
	}
	if c.rxBid < 0 {
		if f.rx == nil {
			f.closeConn(c)
			return
		}
		bid, ok := f.rx.Take()
		if !ok {
			f.parkRecv(c)
			return
		}
		c.rxBid = bid
		c.rxStart, c.rxEnd = 0, 0
	}
	buf := f.rx.Bytes(c.rxBid)
	if int(c.rxEnd) == len(buf) {
		if c.rxStart > 0 {
			copy(buf, buf[c.rxStart:c.rxEnd])
			c.rxEnd -= c.rxStart
			c.rxStart = 0
		}
		if int(c.rxEnd) == len(buf) {
			f.closeConn(c)
			return
		}
	}
	sqe := f.getSQE()
	if sqe == nil {
		f.closeConn(c)
		return
	}
	sqe.PrepareRecv(int(c.fd), uintptr(unsafe.Pointer(&buf[c.rxEnd])), uint32(len(buf)-int(c.rxEnd)), 0)
	sqe.UserData = packUser(rawOpRecv, c.id, c.gen)
	c.set(flagRecv)
	c.clear(flagParkedRecv)
}

func (f *fastLoop) parkRecv(c *fastConn) {
	if c.has(flagParkedRecv) {
		return
	}
	c.set(flagParkedRecv)
	f.parkedRecv = append(f.parkedRecv, c.id)
}

func (f *fastLoop) wakeParkedRecv() {
	if f.rx == nil || f.parkedRecvBusy {
		return
	}
	f.parkedRecvBusy = true
	defer func() { f.parkedRecvBusy = false }()
	for f.rx.FreeCount() > 0 && len(f.parkedRecv) > 0 {
		n := len(f.parkedRecv) - 1
		id := f.parkedRecv[n]
		f.parkedRecv = f.parkedRecv[:n]
		if id >= f.slots.next {
			continue
		}
		c := f.slots.get(id)
		if !c.has(flagLive) || !c.has(flagParkedRecv) {
			continue
		}
		c.clear(flagParkedRecv)
		f.submitRecv(c)
	}
	if len(f.parkedRecv) == 0 && cap(f.parkedRecv) > 4096 {
		f.parkedRecv = nil
	}
}

func (f *fastLoop) handleRecv(slot, gen uint32, res int32) {
	c := f.slotFor(slot, gen)
	if c == nil {
		return
	}
	c.clear(flagRecv)
	if !c.has(flagLive) {
		// The connection closed while this receive was outstanding. The
		// buffer must not be recycled before the kernel is done with it.
		f.releaseRX(c)
		f.tryRelease(c, gen)
		return
	}
	if res == -int32(unix.EAGAIN) {
		if c.rxEnd == 0 {
			f.releaseRX(c)
		}
		f.submitPoll(c)
		return
	}
	if res <= 0 {
		f.closeConn(c)
		return
	}
	f.disarmTimer(c, kindIdle)
	c.rxEnd += res
	f.processRequests(c)
	if !c.has(flagLive) || c.has(flagCloseAfter) || c.has(flagHTTPPending) || c.has(flagSend) || c.has(flagParkedSend) || c.txLen > 0 {
		return
	}
	if c.rxBid >= 0 && c.rxStart < c.rxEnd {
		f.submitPoll(c)
		return
	}
	f.releaseRX(c)
	f.disarmTimer(c, kindHeader)
	f.armTimer(c, kindIdle, f.server.IdleTimeout)
	f.submitPoll(c)
}

func (f *fastLoop) processRequests(c *fastConn) {
	for c.has(flagLive) {
		// A parked send already owns pending response bytes; stop parsing
		// more pipelined requests into an unbounded queue.
		if c.has(flagParkedSend) || int(c.txLen) >= f.maxOut() {
			return
		}
		consumed, need, ok := f.parse(c)
		if !ok {
			f.closeConn(c)
			return
		}
		if need {
			// Bound the whole request (headers and body), not just the gap
			// between reads, so a slow-drip client cannot hold a buffer
			// indefinitely by sending one byte per timeout window.
			if c.timers[kindHeader] == timerwheel.NoNode {
				f.armTimer(c, kindHeader, f.server.HeaderTimeout)
			}
			return
		}
		f.disarmTimer(c, kindHeader)
		c.rxStart += int32(consumed)
		c.hdrEnd = -1
		c.hdrBits = 0
		c.scanOff = 0
		if c.rxStart == c.rxEnd {
			c.rxStart, c.rxEnd = 0, 0
		}
		if c.has(flagHTTPPending) || c.has(flagCloseAfter) || c.has(flagSend) || c.has(flagParkedSend) {
			return
		}
	}
}

func (f *fastLoop) submitSend(c *fastConn) {
	if c == nil || !c.has(flagLive) || c.has(flagSend) || c.txOff >= c.txLen || c.fd < 0 {
		return
	}
	sqe := f.getSQE()
	if sqe == nil {
		if f.ring == nil {
			c.set(flagSend)
			return
		}
		f.closeConn(c)
		return
	}
	var ptr unsafe.Pointer
	var n uint32
	if c.txHeap != nil {
		ptr = unsafe.Pointer(&c.txHeap[c.txOff])
		n = uint32(len(c.txHeap) - int(c.txOff))
	} else if c.txBid >= 0 && f.tx != nil {
		b := f.tx.Bytes(c.txBid)
		ptr = unsafe.Pointer(&b[c.txOff])
		n = uint32(c.txLen - c.txOff)
	} else {
		return
	}
	if n == 0 {
		return
	}
	sqe.PrepareSend(int(c.fd), uintptr(ptr), n, unix.MSG_NOSIGNAL)
	sqe.UserData = packUser(rawOpSend, c.id, c.gen)
	c.set(flagSend)
	// Arm once per response: WriteTimeout is an absolute deadline for the
	// response, not a per-chunk inactivity budget. Re-arming on every partial
	// send would let a slow reader hold an arena block indefinitely.
	if c.timers[kindWrite] == timerwheel.NoNode {
		f.armTimer(c, kindWrite, f.server.WriteTimeout)
	}
}

func (f *fastLoop) handleSend(slot, gen uint32, res int32) {
	c := f.slotFor(slot, gen)
	if c == nil {
		return
	}
	c.clear(flagSend)
	if !c.has(flagLive) {
		// The connection closed while this send was outstanding. The block
		// may still be read by the kernel, so it is released only now.
		f.releaseTX(c)
		f.tryRelease(c, gen)
		return
	}
	if res <= 0 {
		f.closeConn(c)
		return
	}
	c.txOff += res
	if c.txOff < c.txLen {
		f.submitSend(c)
		return
	}
	f.disarmTimer(c, kindWrite)
	pending := []byte(nil)
	if c.extra != nil && len(c.extra.pending) > 0 {
		pending = c.extra.pending
		c.extra.pending = nil
	}
	f.releaseTX(c)
	if c.reqStart != 0 && f.server.OnRequest != nil {
		f.server.OnRequest(time.Duration(time.Now().UnixNano() - c.reqStart))
		c.reqStart = 0
	}
	if len(pending) > 0 {
		f.dropExtra(c)
		f.beginSend(c, pending)
		return
	}
	f.dropExtra(c)
	if c.has(flagCloseAfter) {
		f.closeConn(c)
		return
	}
	if c.rxBid >= 0 && c.rxStart < c.rxEnd {
		f.processRequests(c)
		if c.has(flagLive) && !c.has(flagSend) && !c.has(flagHTTPPending) && !c.has(flagCloseAfter) && !c.has(flagParkedSend) {
			f.submitPoll(c)
		}
		return
	}
	f.releaseRX(c)
	f.armTimer(c, kindIdle, f.server.IdleTimeout)
	f.submitPoll(c)
}

func (f *fastLoop) beginSend(c *fastConn, dst []byte) {
	if c == nil || !c.has(flagLive) {
		return
	}
	if len(dst) == 0 {
		f.closeConn(c)
		return
	}
	if c.txBid >= 0 || c.txHeap != nil {
		if f.extendTX(c, dst) {
			if !c.has(flagSend) {
				f.submitSend(c)
			}
			return
		}
		f.queuePending(c, dst)
		return
	}
	if c.extra != nil && len(c.extra.pending) > 0 {
		f.queuePending(c, dst)
		return
	}
	c.txOff = 0
	if f.tx != nil && len(dst) <= f.tx.Size() {
		bid, ok := f.tx.Take(c.id, c.gen, len(dst))
		if ok {
			copy(f.tx.Bytes(bid), dst)
			c.txBid = bid
			c.txLen = int32(len(dst))
			f.submitSend(c)
			return
		}
		f.queuePending(c, dst)
		if !c.has(flagLive) {
			return
		}
		if !c.has(flagParkedSend) {
			c.set(flagParkedSend)
			f.parkedSend = append(f.parkedSend, c.id)
		}
		return
	}
	// A single response above the arena block size is copied to a private
	// heap block. Pipelined bytes stay bounded by MaxOutputBytes in
	// queuePending, but one legitimate large body is still served.
	buf := make([]byte, len(dst))
	copy(buf, dst)
	c.txHeap = buf
	c.txLen = int32(len(dst))
	f.submitSend(c)
}

// queuePending buffers response bytes behind an in-flight or arena-starved
// send. The total is bounded by MaxOutputBytes; a peer that pipelines requests
// while the send path is blocked therefore cannot grow this without bound.
func (f *fastLoop) queuePending(c *fastConn, dst []byte) {
	if int(c.txLen)+len(dst) > f.maxOut() {
		f.closeConn(c)
		return
	}
	if c.extra == nil {
		c.extra = &httpExtra{}
	}
	if len(c.extra.pending)+len(dst) > f.maxOut() {
		f.closeConn(c)
		return
	}
	c.extra.pending = append(c.extra.pending, dst...)
}

func (f *fastLoop) extendTX(c *fastConn, dst []byte) bool {
	if int(c.txLen)+len(dst) > f.maxOut() {
		return false
	}
	if c.has(flagSend) && c.txHeap != nil {
		return false
	}
	if c.txHeap != nil {
		c.txHeap = append(c.txHeap, dst...)
		c.txLen = int32(len(c.txHeap))
		return true
	}
	if c.txBid < 0 || f.tx == nil {
		return false
	}
	block := f.tx.Bytes(c.txBid)
	if int(c.txLen)+len(dst) > len(block) {
		return false
	}
	copy(block[c.txLen:], dst)
	c.txLen += int32(len(dst))
	return true
}

func (f *fastLoop) wakeParkedSend() {
	if f.tx == nil || f.parkedSendBusy {
		return
	}
	f.parkedSendBusy = true
	defer func() { f.parkedSendBusy = false }()
	kept := f.parkedSend[:0]
	blocked := false
	for _, id := range f.parkedSend {
		if blocked {
			kept = append(kept, id)
			continue
		}
		if id >= f.slots.next {
			continue
		}
		c := f.slots.get(id)
		if !c.has(flagLive) || !c.has(flagParkedSend) {
			continue
		}
		if c.extra == nil || len(c.extra.pending) == 0 {
			c.clear(flagParkedSend)
			continue
		}
		if c.txBid >= 0 || c.txHeap != nil {
			kept = append(kept, id)
			continue
		}
		if len(c.extra.pending) > f.tx.Size() {
			c.clear(flagParkedSend)
			buf := c.extra.pending
			c.extra.pending = nil
			f.dropExtra(c)
			f.beginSend(c, buf)
			continue
		}
		bid, ok := f.tx.Take(c.id, c.gen, len(c.extra.pending))
		if !ok {
			blocked = true
			kept = append(kept, id)
			continue
		}
		copy(f.tx.Bytes(bid), c.extra.pending)
		c.txBid = bid
		c.txLen = int32(len(c.extra.pending))
		c.txOff = 0
		c.extra.pending = nil
		c.clear(flagParkedSend)
		f.dropExtra(c)
		f.submitSend(c)
	}
	f.parkedSend = kept
	if len(f.parkedSend) == 0 && cap(f.parkedSend) > 4096 {
		f.parkedSend = nil
	}
}

func (f *fastLoop) releaseRX(c *fastConn) {
	if c == nil || c.rxBid < 0 || c.has(flagHTTPPending) || c.has(flagRecv) {
		return
	}
	if f.rx != nil {
		f.rx.Put(c.rxBid)
	}
	c.rxBid = noBid
	c.rxStart, c.rxEnd = 0, 0
	c.hdrEnd = -1
	c.hdrBits = 0
	c.scanOff = 0
	f.wakeParkedRecv()
}

func (f *fastLoop) releaseTX(c *fastConn) {
	if c == nil || c.has(flagSend) {
		return
	}
	if c.txBid >= 0 && f.tx != nil {
		f.tx.Release(c.txBid, c.id, c.gen)
	}
	c.txBid = noBid
	c.txHeap = nil
	c.txOff, c.txLen = 0, 0
	f.wakeParkedSend()
}

func (f *fastLoop) dropExtra(c *fastConn) {
	if c == nil || c.extra == nil {
		return
	}
	if c.extra.job != nil || len(c.extra.pending) > 0 {
		return
	}
	c.extra = nil
}

// tryRelease returns a closed slot to the free list only after every
// operation that still references it has completed. gen is the generation the
// caller observed; a second waiter for the same close finds a bumped
// generation and does nothing.
func (f *fastLoop) tryRelease(c *fastConn, gen uint32) {
	if c == nil || c.gen != gen {
		return
	}
	if c.has(flagLive | flagRecv | flagSend | flagPoll | flagHTTPPending) {
		return
	}
	if c.fd >= 0 {
		// The descriptor is closed before a slot becomes releasable; a live
		// fd here means a failed close is waiting for shutdown teardown.
		return
	}
	f.slots.release(c.id)
}

func (f *fastLoop) closeConn(c *fastConn) {
	if c == nil || !c.has(flagLive) {
		return
	}
	c.clear(flagLive)
	f.disarmAll(c)
	if c.fd >= 0 {
		if f.ringBroken {
			// Leave the descriptor open for shutdown teardown; submitting
			// again could resolve a queued SQE after the fd is reused.
			return
		}
		// Flush prepared SQEs before closing. io_uring resolves descriptors
		// at submission time and holds a file reference afterwards, so a
		// deferred send/recv/poll cannot target a reused descriptor number.
		if f.ring != nil {
			if _, err := f.ring.Submit(); err != nil {
				f.ringBroken = true
				f.stop.Store(true)
				return
			}
		}
		if c.has(flagRecv | flagSend | flagPoll) {
			// Wake a blocked receive, send, or poll so its completion arrives
			// and the buffer/slot can be released safely.
			_ = unix.Shutdown(int(c.fd), unix.SHUT_RDWR)
		}
		_ = unix.Close(int(c.fd))
		c.fd = -1
	}
	f.releaseRX(c)
	f.releaseTX(c)
	f.tryRelease(c, c.gen)
}

func (f *fastLoop) closeAll() {
	f.stop.Store(true)
	if f.ring != nil && !f.ringBroken {
		// Submit prepared operations while every descriptor is still open.
		if _, err := f.ring.Submit(); err != nil {
			f.ringBroken = true
		}
	}
	for _, ch := range f.slots.chunks {
		for i := range ch {
			c := &ch[i]
			if !c.has(flagLive) && c.fd < 0 {
				continue
			}
			f.disarmAll(c)
			if c.fd >= 0 {
				if c.has(flagRecv | flagSend | flagPoll) {
					_ = unix.Shutdown(int(c.fd), unix.SHUT_RDWR)
				}
				_ = unix.Close(int(c.fd))
				c.fd = -1
			}
			c.clear(flagLive)
		}
	}
	if f.ringBroken {
		// Never submit or unmap after a failed flush; ring teardown owns the
		// outstanding operations and memory.
		f.slots = slotTable{}
		return
	}
	// Reap outstanding receives, sends, and polls before unmapping the
	// arenas. If the deadline expires, keep the arenas mapped: the ring is
	// torn down afterwards and may still reference that memory.
	drained := f.drainInflight()
	f.slots = slotTable{}
	if !drained {
		return
	}
	if f.rx != nil {
		_ = f.rx.Close()
		f.rx = nil
	}
	if f.tx != nil {
		_ = f.tx.Close()
		f.tx = nil
	}
}

func (f *fastLoop) drainInflight() bool {
	if f.ring == nil {
		return true
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	ts := syscall.NsecToTimespec(int64(time.Millisecond))
	for time.Now().Before(deadline) {
		pending := false
		for _, ch := range f.slots.chunks {
			for i := range ch {
				if ch[i].has(flagRecv | flagSend | flagPoll) {
					pending = true
					break
				}
			}
			if pending {
				break
			}
		}
		if !pending {
			return true
		}
		if _, err := f.ring.SubmitAndWaitTimeout(1, &ts, nil); err != nil {
			if errors.Is(err, unix.ETIME) || errors.Is(err, unix.EINTR) {
				continue
			}
			return false
		}
		f.processCQEs()
	}
	return false
}

func (f *fastLoop) maxOut() int {
	if f.server != nil && f.server.MaxOutputBytes > 0 {
		return f.server.MaxOutputBytes
	}
	return 256 << 10
}

func (f *fastLoop) rxData(c *fastConn) []byte {
	if c.rxBid < 0 || f.rx == nil {
		return nil
	}
	b := f.rx.Bytes(c.rxBid)
	return b[c.rxStart:c.rxEnd]
}

func (f *fastLoop) parse(c *fastConn) (consumed int, need bool, ok bool) {
	data := f.rxData(c)

	// Reuse the header terminator found by an earlier parse of the same
	// buffered request; bytes before rxEnd are immutable until consumed or
	// compacted, so only the four bytes at the cached offset need checking.
	headerEnd := -1
	if n := int(c.hdrEnd); n >= 0 && n+3 < len(data) &&
		data[n] == '\r' && data[n+1] == '\n' && data[n+2] == '\r' && data[n+3] == '\n' {
		headerEnd = n
	}
	if headerEnd < 0 {
		start := int(c.scanOff)
		if start > 3 {
			start -= 3 // overlap so a terminator split across reads is found
		} else {
			start = 0
		}
		headerEnd = indexHeaderEndFrom(data, start)
		if headerEnd < 0 {
			if len(data) >= fastInSize-1 {
				return 0, false, false
			}
			// Everything scanned so far is known not to start a terminator;
			// the next parse resumes near the end instead of rescanning.
			c.scanOff = int32(len(data))
			c.hdrEnd = -1
			c.hdrBits = 0
			return 0, true, true
		}
		c.hdrEnd = int32(headerEnd)
		c.hdrBits = 0
	}

	lineEnd := bytes.IndexByte(data, '\r')
	if lineEnd < 0 || lineEnd > headerEnd {
		return 0, false, false
	}
	if lineEnd+1 >= len(data) || data[lineEnd+1] != '\n' {
		return 0, false, false
	}
	sp1 := bytes.IndexByte(data[:lineEnd], ' ')
	if sp1 <= 0 {
		return 0, false, false
	}
	sp2 := bytes.IndexByte(data[sp1+1:lineEnd], ' ')
	if sp2 <= 0 {
		return 0, false, false
	}
	sp2 += sp1 + 1

	method := data[:sp1]
	target := data[sp1+1 : sp2]
	proto := data[sp2+1 : lineEnd]
	if !validTokenBytes(method) || !validTargetBytes(target) {
		return 0, false, false
	}
	if !protoHTTP11(proto) && !protoHTTP10(proto) {
		return 0, false, false
	}
	path := target
	if q := bytes.IndexByte(path, '?'); q >= 0 {
		path = path[:q]
	}
	if len(path) == 0 || path[0] != '/' {
		return 0, false, false
	}

	headerStart := lineEnd + 2
	headerStop := headerEnd
	if headerStop < headerStart {
		// A request with no header lines terminates immediately after the
		// request line: indexHeaderEnd points at the request-line CR, so the
		// header block is the empty slice at headerStart, not a reversed one.
		headerStop = headerStart
	}
	if headerStop > len(data) {
		return 0, false, false
	}

	var contentLength int
	var closeConn, keepAlive bool
	if c.hdrBits&hdrParsed == 0 {
		if !validCRLFBlock(data[:headerEnd+4]) {
			// Bare CR or LF bytes let two hops disagree about message
			// boundaries. This parser only accepts CRLF framing.
			return 0, false, false
		}
		header := data[headerStart:headerStop]
		var contentLenSet, transferEnc, hostSet, expectContinue bool
		for len(header) > 0 {
			line := header
			if i := bytes.IndexByte(header, '\r'); i >= 0 {
				// validCRLFBlock guarantees a following LF.
				line = header[:i]
				header = header[i+2:]
			} else {
				// The final header line is terminated by the first CRLF of
				// the CRLFCRLF header terminator, so it carries no CR itself.
				header = nil
			}
			if len(line) == 0 {
				return 0, false, false
			}
			// Reject obs-fold and whitespace before the colon. Frameworks
			// that proxy or normalize such headers can otherwise disagree
			// with this parser about message boundaries (request smuggling).
			if line[0] == ' ' || line[0] == '\t' {
				return 0, false, false
			}
			colon := bytes.IndexByte(line, ':')
			if colon <= 0 {
				return 0, false, false
			}
			if line[colon-1] == ' ' || line[colon-1] == '\t' {
				return 0, false, false
			}
			name := line[:colon]
			value := bytes.TrimSpace(line[colon+1:])
			if !validTokenBytes(name) || !validFieldValueBytes(value) {
				return 0, false, false
			}
			switch {
			case equalFold(name, "Content-Length"):
				// A duplicate or conflicting length is ambiguous between hops.
				if contentLenSet {
					return 0, false, false
				}
				n, perr := parseDecimal(value)
				if !perr {
					return 0, false, false
				}
				contentLength = n
				contentLenSet = true
			case equalFold(name, "Transfer-Encoding"):
				// This server does not decode chunked bodies; accepting the
				// header without decoding would desynchronize the stream.
				transferEnc = true
			case equalFold(name, "Host"):
				if hostSet {
					return 0, false, false
				}
				hostSet = true
			case equalFold(name, "Expect"):
				if equalFold(value, "100-continue") {
					expectContinue = true
				}
			case equalFold(name, "Connection"):
				if headerHasToken(value, "close") {
					closeConn = true
				}
				if headerHasToken(value, "keep-alive") {
					keepAlive = true
				}
			}
		}
		if transferEnc {
			return 0, false, false
		}
		c.hdrLen = int32(contentLength)
		c.hdrBits = hdrParsed
		if closeConn {
			c.hdrBits |= hdrClose
		}
		if keepAlive {
			c.hdrBits |= hdrKeepAlive
		}
		if expectContinue {
			c.hdrBits |= hdrExpectContinue
		}
	} else {
		contentLength = int(c.hdrLen)
		closeConn = c.hdrBits&hdrClose != 0
		keepAlive = c.hdrBits&hdrKeepAlive != 0
	}

	bodyStart := headerEnd + 4
	limit := fastInSize
	if f.rx != nil {
		limit = f.rx.Size()
	}
	if contentLength > 0 && bodyStart+contentLength > limit {
		// The body can never fit the connection buffer. Answer once and
		// close instead of holding the connection until HeaderTimeout.
		c.set(flagCloseAfter)
		f.build = f.build[:0]
		f.build = append(f.build, payloadTooLargeResponse...)
		f.beginSend(c, f.build)
		f.trimBuild()
		return 0, true, true
	}
	if len(data) < bodyStart+contentLength {
		if c.hdrBits&hdrExpectContinue != 0 && c.hdrBits&hdrContinueSent == 0 {
			c.hdrBits |= hdrContinueSent
			f.build = f.build[:0]
			f.build = append(f.build, continueResponse...)
			f.beginSend(c, f.build)
			f.trimBuild()
		}
		return 0, true, true
	}
	body := data[bodyStart : bodyStart+contentLength]
	if f.server.OnRequest != nil {
		c.reqStart = time.Now().UnixNano()
	}
	if f.server.HTTPHandler != nil {
		if c.extra == nil {
			c.extra = &httpExtra{}
		}
		c.extra.httpConsumed = bodyStart + contentLength
		query := target
		if q := bytes.IndexByte(target, '?'); q >= 0 {
			query = target[q+1:]
		} else {
			query = nil
		}
		// HTTP/1.0 closes by default unless the request opted into keep-alive.
		httpClose := closeConn || (!protoHTTP11(proto) && !keepAlive)
		f.dispatchHTTP(c, method, target, path, query, proto, data[headerStart:headerStop], body, contentLength, httpClose)
		return bodyStart + contentLength, false, true
	}

	if f.respBody == nil {
		f.respBody = make([]byte, 0, 512)
	}
	req := &f.req
	*req = Request{
		Method: method,
		Path:   path,
		Proto:  proto,
		Header: data[headerStart:headerStop],
		Body:   body,
	}
	res := &f.res
	*res = Response{Status: 200, Body: f.respBody[:0]}
	func() {
		defer func() {
			if recover() != nil {
				res.Status = http.StatusInternalServerError
				res.Body = res.Body[:0]
			}
		}()
		f.server.Handler.ServeHTTP(req, res)
	}()
	if res.Status == 0 {
		res.Status = 200
	}
	if res.ContentType == nil {
		res.ContentType = defaultContentType
	}
	keep := !closeConn && (protoHTTP11(proto) || keepAlive)
	if res.Close {
		keep = false
	}
	f.queueResponse(c, res, keep, isHead(method))
	if !keep {
		c.set(flagCloseAfter)
	}
	if cap(f.respBody) > 2*fastInSize {
		f.respBody = nil
	}
	return bodyStart + contentLength, false, true
}

func protoHTTP11(proto []byte) bool {
	return len(proto) == 8 && proto[0] == 'H' && proto[1] == 'T' && proto[2] == 'T' && proto[3] == 'P' &&
		proto[4] == '/' && proto[5] == '1' && proto[6] == '.' && proto[7] == '1'
}

func protoHTTP10(proto []byte) bool {
	return len(proto) == 8 && proto[0] == 'H' && proto[1] == 'T' && proto[2] == 'T' && proto[3] == 'P' &&
		proto[4] == '/' && proto[5] == '1' && proto[6] == '.' && proto[7] == '0'
}

func isHead(method []byte) bool {
	return len(method) == 4 && method[0] == 'H' && method[1] == 'E' && method[2] == 'A' && method[3] == 'D'
}

func indexHeaderEndFrom(b []byte, start int) int {
	if start < 0 {
		start = 0
	}
	for i := start; i+3 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' && b[i+2] == '\r' && b[i+3] == '\n' {
			return i
		}
	}
	return -1
}

// validCRLFBlock reports whether every CR is followed by LF and every LF is
// preceded by CR.
func validCRLFBlock(b []byte) bool {
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '\r':
			if i+1 >= len(b) || b[i+1] != '\n' {
				return false
			}
			i++
		case '\n':
			return false
		}
	}
	return true
}

func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

func validTokenBytes(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if !isTokenChar(c) {
			return false
		}
	}
	return true
}

// validTargetBytes rejects control bytes, spaces, and invalid percent escapes
// in the request target.
func validTargetBytes(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == '%' {
			if i+2 >= len(b) || !isHexByte(b[i+1]) || !isHexByte(b[i+2]) {
				return false
			}
			i += 2
			continue
		}
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

func isHexByte(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		return true
	}
	return false
}

// validFieldValueBytes accepts visible ASCII, obs-text, and internal tabs.
func validFieldValueBytes(b []byte) bool {
	for _, c := range b {
		if c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// headerHasToken reports whether a comma-separated header value contains the
// token, using the same list semantics as net/http.
func headerHasToken(value []byte, token string) bool {
	for len(value) > 0 {
		tok := value
		if i := bytes.IndexByte(value, ','); i >= 0 {
			tok, value = value[:i], value[i+1:]
		} else {
			value = nil
		}
		if equalFold(bytes.TrimSpace(tok), token) {
			return true
		}
	}
	return false
}

func equalFold(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		d := s[i]
		if d >= 'A' && d <= 'Z' {
			d += 'a' - 'A'
		}
		if c != d {
			return false
		}
	}
	return true
}

func parseDecimal(b []byte) (int, bool) {
	if len(b) == 0 {
		return 0, false
	}
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return 0, false
		}
	}
	return n, true
}

func (f *fastLoop) trimBuild() {
	f.build = f.build[:0]
	if cap(f.build) > 2*fastInSize {
		f.build = nil
	}
}

func (f *fastLoop) queueResponse(c *fastConn, res *Response, keep, head bool) {
	f.build = f.build[:0]
	f.build = appendResponse(f.build, res, keep, head, f.serverHeader(), f.dateHeader())
	f.beginSend(c, f.build)
	f.trimBuild()
}

func (f *fastLoop) serverHeader() string {
	if f.server != nil && f.server.ServerHeader != "" {
		return f.server.ServerHeader
	}
	return "httpuring"
}

func appendResponse(dst []byte, res *Response, keep, head bool, serverHeader string, date []byte) []byte {
	code := res.Status
	if code < 100 || code > 999 {
		code = 500
	}
	noBody := head || code < 200 || code == 204 || code == 304
	dst = append(dst, httpPrefix...)
	dst = strconv.AppendInt(dst, int64(code), 10)
	dst = append(dst, ' ')
	dst = append(dst, statusText(code)...)
	dst = append(dst, serverPrefix...)
	dst = appendSanitizedString(dst, serverHeader)
	dst = append(dst, datePrefix...)
	dst = append(dst, date...)
	dst = append(dst, contentTypePrefix...)
	dst = appendSanitizedValue(dst, res.ContentType)
	switch {
	case code < 200 || code == 204:
		// 1xx and 204 responses must not carry Content-Length.
	case code == 304:
		// Raw responses cannot supply a representation length, and 304 must
		// not get a synthesized Content-Length.
	case head:
		// Keep the representation length for HEAD only when a body was set.
		// An empty HEAD body must not become Content-Length: 0.
		if len(res.Body) > 0 {
			dst = append(dst, contentLengthPrefix...)
			dst = strconv.AppendInt(dst, int64(len(res.Body)), 10)
		}
	default:
		dst = append(dst, contentLengthPrefix...)
		dst = strconv.AppendInt(dst, int64(len(res.Body)), 10)
	}
	if !keep {
		dst = append(dst, closePrefix...)
	}
	dst = append(dst, crlf...)
	if !noBody {
		dst = append(dst, res.Body...)
	}
	return dst
}

var (
	httpPrefix              = []byte("HTTP/1.1 ")
	serverPrefix            = []byte("\r\nServer: ")
	datePrefix              = []byte("\r\nDate: ")
	contentTypePrefix       = []byte("\r\nContent-Type: ")
	contentLengthPrefix     = []byte("\r\nContent-Length: ")
	closePrefix             = []byte("\r\nConnection: close")
	crlf                    = []byte("\r\n\r\n")
	defaultContentType      = []byte("text/plain; charset=utf-8")
	continueResponse        = []byte("HTTP/1.1 100 Continue\r\n\r\n")
	payloadTooLargeResponse = []byte("HTTP/1.1 413 Payload Too Large\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
)

// appendSanitizedValue appends a header value up to the first control byte so
// application data can never inject a new header line or a second response.
func appendSanitizedValue(dst, v []byte) []byte {
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return append(dst, v[:i]...)
		}
	}
	return append(dst, v...)
}

func appendSanitizedString(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return append(dst, s[:i]...)
		}
	}
	return append(dst, s...)
}

func validHeaderName(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		if !isTokenChar(k[i]) {
			return false
		}
	}
	return true
}

func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func appendString(dst []byte, s string) []byte {
	n := len(dst)
	if cap(dst)-n < len(s) {
		dst = append(dst, make([]byte, len(s))...)
	}
	dst = dst[:n+len(s)]
	copy(dst[n:], s)
	return dst
}

func (f *fastLoop) dateHeader() []byte {
	if f.dateCount == 0 || len(f.date) == 0 {
		now := time.Now()
		f.dateUnix = now.Unix()
		f.date = now.UTC().AppendFormat(f.date[:0], httpDateFmt)
		f.dateCount = 4096
	}
	f.dateCount--
	return f.date
}

func statusText(code int) []byte {
	switch code {
	case 200:
		return []byte("OK")
	case 201:
		return []byte("Created")
	case 204:
		return []byte("No Content")
	case 304:
		return []byte("Not Modified")
	case 400:
		return []byte("Bad Request")
	case 404:
		return []byte("Not Found")
	case 405:
		return []byte("Method Not Allowed")
	case 413:
		return []byte("Payload Too Large")
	case 500:
		return []byte("Internal Server Error")
	case 401:
		return []byte("Unauthorized")
	default:
		return []byte("Status")
	}
}
