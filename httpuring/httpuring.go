//go:build linux

// Package httpuring adapts encryl's io_uring runtime to HTTP.
//
// Listen/Conn provide a net.Listener/net.Conn stream transport for net/http or
// fasthttp. Server is the direct HTTP/1.1 loop: it parses requests and writes
// responses on the ring without net/http, fasthttp, per-connection goroutines,
// or channel handoffs. It is not the WebSocket reactor and does not parse
// frames.
package httpuring

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/encryptedbusiness/encryl/netio"
	"github.com/encryptedbusiness/encryl/uring"
	"github.com/encryptedbusiness/encryl/uring/giouring"
	"golang.org/x/sys/unix"
)

const (
	opAccept uint8 = iota + 1
	opRead
	opWrite
	opWake
	opCancel
)

const idMask = uint64(1)<<56 - 1

func encode(op uint8, id uint64) uint64 {
	return uint64(op)<<56 | id&idMask
}

func decode(ud uint64) (op uint8, id uint64) {
	return uint8(ud >> 56), ud & idMask
}

// Listener is a net.Listener backed by one io_uring reactor.
type Listener struct {
	ln   netio.Listener
	addr net.Addr

	acceptCh   chan net.Conn
	reqCh      chan *request
	done       chan struct{}
	acceptDone chan struct{}
	stopOnce   sync.Once
	acceptOnce sync.Once

	acceptClosed atomic.Bool
	pending      atomic.Int64

	connMu sync.Mutex
	conns  map[*Conn]struct{}

	wakeFD      atomic.Int32
	wakePending atomic.Bool

	errMu sync.Mutex
	err   error
}

type request struct {
	conn     *Conn
	p        []byte
	off      int
	n        int
	err      error
	write    bool
	cancel   bool
	cancelID uint64
}

// Listen binds addr and starts the io_uring accept/read/write loop.
func Listen(addr string) (net.Listener, error) {
	ln, err := netio.Listen(addr, false, 65535)
	if err != nil {
		return nil, err
	}
	l := &Listener{
		ln:         ln,
		addr:       ln.Addr(),
		acceptCh:   make(chan net.Conn, 128),
		reqCh:      make(chan *request, 1024),
		done:       make(chan struct{}),
		acceptDone: make(chan struct{}),
		conns:      make(map[*Conn]struct{}),
	}
	l.wakeFD.Store(-1)
	go l.run()
	return l, nil
}

func (l *Listener) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer l.stop()
	defer close(l.acceptCh)

	ring, err := uring.NewRing(4096)
	if err != nil {
		l.fail(fmt.Errorf("httpuring: ring setup: %w", err))
		return
	}
	defer ring.QueueExit()

	wakeFD, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		l.fail(fmt.Errorf("httpuring: eventfd: %w", err))
		return
	}
	l.wakeFD.Store(int32(wakeFD))
	defer func() {
		l.wakeFD.Store(-1)
		_ = unix.Close(wakeFD)
	}()

	state := &loop{
		l:       l,
		ring:    ring,
		pending: make(map[uint64]*request),
	}
	defer state.close()

	if !state.armWake() {
		l.fail(errors.New("httpuring: eventfd poll submission failed"))
		return
	}
	state.submitAccept()
	for {
		if state.stopped() {
			return
		}
		state.drainRequests()
		if !state.acceptPending {
			state.submitAccept()
		}
		if state.stopped() {
			return
		}
		if _, err := ring.SubmitAndWait(1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			l.fail(fmt.Errorf("httpuring: submit: %w", err))
			return
		}
		state.processCQEs()
		l.maybeStop()
	}
}

type loop struct {
	l       *Listener
	ring    *giouring.Ring
	pending map[uint64]*request
	nextID  uint64

	acceptPending bool
	wakeArmed     bool
}

func (s *loop) stopped() bool {
	select {
	case <-s.l.done:
		return true
	default:
		return false
	}
}

func (s *loop) close() {
	s.l.stop()
}

func (l *Listener) stop() {
	l.stopOnce.Do(func() {
		l.acceptOnce.Do(func() { close(l.acceptDone) })
		close(l.done)
		l.wake()
	})
}

func (l *Listener) closeAccept() {
	l.acceptClosed.Store(true)
	l.acceptOnce.Do(func() { close(l.acceptDone) })
}

func (l *Listener) maybeStop() {
	if !l.acceptClosed.Load() || len(l.reqCh) > 0 || l.pending.Load() > 0 {
		return
	}
	l.connMu.Lock()
	n := len(l.conns)
	l.connMu.Unlock()
	if n == 0 {
		l.stop()
	}
}

func (l *Listener) fail(err error) {
	l.errMu.Lock()
	if l.err == nil {
		l.err = err
	}
	l.errMu.Unlock()
	l.stop()
}

func (l *Listener) getErr() error {
	l.errMu.Lock()
	defer l.errMu.Unlock()
	if l.err != nil {
		return l.err
	}
	return net.ErrClosed
}

func (l *Listener) wake() {
	if !l.wakePending.CompareAndSwap(false, true) {
		return
	}
	fd := l.wakeFD.Load()
	if fd < 0 {
		return
	}
	var b [8]byte
	*(*uint64)(unsafe.Pointer(&b[0])) = 1
	_, _ = unix.Write(int(fd), b[:])
}

// Accept implements net.Listener.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c, ok := <-l.acceptCh:
		if !ok {
			return nil, l.getErr()
		}
		return c, nil
	case <-l.acceptDone:
		return nil, l.getErr()
	case <-l.done:
		return nil, l.getErr()
	}
}

// Close implements net.Listener. Existing connections keep running until they
// are closed; net/http calls Close on those during Shutdown.
func (l *Listener) Close() error {
	l.closeAccept()
	err := l.ln.Close()
	l.wake()
	l.maybeStop()
	return err
}

// Addr implements net.Listener.
func (l *Listener) Addr() net.Addr { return l.addr }

func (s *loop) getSQE() *giouring.SubmissionQueueEntry {
	if sqe := s.ring.GetSQE(); sqe != nil {
		return sqe
	}
	if _, err := s.ring.Submit(); err != nil {
		return nil
	}
	return s.ring.GetSQE()
}

func (s *loop) armWake() bool {
	if s.wakeArmed {
		return true
	}
	sqe := s.getSQE()
	if sqe == nil {
		return false
	}
	sqe.PreparePollAdd(int(s.l.wakeFD.Load()), unix.POLLIN)
	sqe.UserData = encode(opWake, 0)
	s.wakeArmed = true
	return true
}

func (s *loop) submitAccept() {
	if s.acceptPending || s.stopped() {
		return
	}
	sqe := s.getSQE()
	if sqe == nil {
		return
	}
	sqe.PrepareAccept(s.l.ln.FD(), 0, 0, unix.SOCK_CLOEXEC)
	sqe.UserData = encode(opAccept, 0)
	s.acceptPending = true
}

func (s *loop) drainRequests() {
	for {
		s.l.wakePending.Store(false)
		select {
		case r := <-s.l.reqCh:
			s.submit(r)
		default:
			s.l.maybeStop()
			return
		}
	}
}

func (s *loop) submit(r *request) {
	if r == nil || r.conn == nil {
		return
	}
	if r.cancel {
		s.submitCancel(r)
		return
	}
	if r.conn.closed.Load() {
		s.finish(r, 0, net.ErrClosed)
		return
	}
	if len(r.p) == 0 {
		s.finish(r, 0, nil)
		return
	}
	id := s.nextID
	s.nextID++
	s.pending[id] = r
	s.l.pending.Add(1)

	var ok bool
	if r.write {
		_, ok = s.submitWrite(id, r)
	} else {
		_, ok = s.submitRead(id, r)
	}
	if !ok {
		delete(s.pending, id)
		err := net.ErrClosed
		if !r.write && r.conn.readExpired.Load() {
			err = os.ErrDeadlineExceeded
		}
		s.finish(r, r.n, err)
	}
}

func (s *loop) submitRead(id uint64, r *request) (*giouring.SubmissionQueueEntry, bool) {
	if r.conn.readExpired.Load() {
		return nil, false
	}
	sqe := s.getSQE()
	if sqe == nil {
		return nil, false
	}
	sqe.PrepareRecv(r.conn.fd, uintptr(unsafe.Pointer(&r.p[0])), uint32(len(r.p)), 0)
	sqe.UserData = encode(opRead, id)
	r.conn.readID.Store(encode(opRead, id))
	return sqe, true
}

func (s *loop) submitCancel(r *request) {
	if r.cancelID == 0 {
		return
	}
	sqe := s.getSQE()
	if sqe == nil {
		return
	}
	sqe.PrepareCancel64(r.cancelID, 0)
	sqe.UserData = encode(opCancel, 0)
}

func (s *loop) submitWrite(id uint64, r *request) (*giouring.SubmissionQueueEntry, bool) {
	if r.off >= len(r.p) {
		s.finish(r, r.n, nil)
		return nil, true
	}
	sqe := s.getSQE()
	if sqe == nil {
		return nil, false
	}
	sqe.PrepareSend(r.conn.fd, uintptr(unsafe.Pointer(&r.p[r.off])), uint32(len(r.p)-r.off), unix.MSG_NOSIGNAL)
	sqe.UserData = encode(opWrite, id)
	r.conn.writeID.Store(encode(opWrite, id))
	return sqe, true
}

func (s *loop) finish(r *request, n int, err error) {
	r.n = n
	r.err = err
	c := r.conn
	if r.write {
		c.writeMu.Lock()
		c.writeN, c.writeErr, c.writeDone = n, err, true
		c.writeCond.Signal()
		c.writeMu.Unlock()
	} else {
		c.readMu.Lock()
		c.readN, c.readErr, c.readDone = n, err, true
		c.readCond.Signal()
		c.readMu.Unlock()
	}
	s.l.pending.Add(-1)
	s.l.maybeStop()
}

func (s *loop) processCQEs() {
	for s.ring.CQReady() > 0 {
		cqe, err := s.ring.PeekCQE()
		if err != nil {
			return
		}
		s.handle(cqe)
		s.ring.CQESeen(cqe)
	}
}

func (s *loop) handle(cqe *giouring.CompletionQueueEvent) {
	op, id := decode(cqe.UserData)
	switch op {
	case opAccept:
		s.acceptPending = false
		if cqe.Res < 0 {
			if !s.stopped() {
				s.submitAccept()
			}
			return
		}
		s.deliver(int(cqe.Res))
		s.submitAccept()
	case opRead:
		r := s.pending[id]
		delete(s.pending, id)
		if r == nil {
			return
		}
		r.conn.readID.Store(0)
		switch {
		case cqe.Res < 0:
			err := errno(cqe.Res)
			if r.conn.readExpired.Load() && errors.Is(err, net.ErrClosed) {
				err = os.ErrDeadlineExceeded
			}
			s.finish(r, 0, err)
		case cqe.Res == 0:
			s.finish(r, 0, io.EOF)
		default:
			s.finish(r, int(cqe.Res), nil)
		}
	case opWrite:
		r := s.pending[id]
		delete(s.pending, id)
		if r == nil {
			return
		}
		r.conn.writeID.Store(0)
		if cqe.Res <= 0 {
			if cqe.Res < 0 {
				err := errno(cqe.Res)
				if r.conn.writeExpired.Load() && errors.Is(err, net.ErrClosed) {
					err = os.ErrDeadlineExceeded
				}
				s.finish(r, r.n, err)
			} else {
				s.finish(r, r.n, io.ErrUnexpectedEOF)
			}
			return
		}
		r.off += int(cqe.Res)
		r.n += int(cqe.Res)
		if r.off >= len(r.p) {
			s.finish(r, r.n, nil)
			return
		}
		nid := s.nextID
		s.nextID++
		s.pending[nid] = r
		if _, ok := s.submitWrite(nid, r); !ok {
			delete(s.pending, nid)
			s.finish(r, r.n, net.ErrClosed)
		}
	case opWake:
		s.wakeArmed = false
		var b [8]byte
		for {
			if _, err := unix.Read(int(s.l.wakeFD.Load()), b[:]); err != nil {
				break
			}
		}
		s.l.wakePending.Store(false)
		if !s.stopped() && !s.armWake() {
			s.l.fail(errors.New("httpuring: eventfd rearm failed"))
		}
	case opCancel:
		// The cancelled read or write completes separately with ECANCELED.
	}
}

func (s *loop) deliver(fd int) {
	remote := peerAddr(fd)
	c := &Conn{l: s.l, fd: fd, local: s.l.addr, remote: remote}
	c.readCond = sync.NewCond(&c.readMu)
	c.writeCond = sync.NewCond(&c.writeMu)
	s.l.connMu.Lock()
	if s.l.acceptClosed.Load() {
		s.l.connMu.Unlock()
		_ = unix.Close(fd)
		return
	}
	s.l.conns[c] = struct{}{}
	s.l.connMu.Unlock()
	select {
	case s.l.acceptCh <- c:
	case <-s.l.done:
		s.l.dropConn(c)
		_ = unix.Close(fd)
	}
}

func errno(res int32) error {
	err := syscall.Errno(-res)
	switch err {
	case unix.EBADF, unix.ECANCELED:
		return net.ErrClosed
	default:
		return err
	}
}

// Conn is a blocking net.Conn serviced by the listener's io_uring ring.
type Conn struct {
	l      *Listener
	fd     int
	local  net.Addr
	remote net.Addr

	closed       atomic.Bool
	readID       atomic.Uint64
	writeID      atomic.Uint64
	readExpired  atomic.Bool
	writeExpired atomic.Bool
	deadlineMu   sync.Mutex
	readTimer    *time.Timer
	writeTimer   *time.Timer

	readMu   sync.Mutex
	readCond *sync.Cond
	readReq  request
	readN    int
	readErr  error
	readDone bool

	writeMu   sync.Mutex
	writeCond *sync.Cond
	writeReq  request
	writeN    int
	writeErr  error
	writeDone bool
}

// Read implements net.Conn.
func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if c.readExpired.Load() {
		return 0, os.ErrDeadlineExceeded
	}
	c.readMu.Lock()
	c.readReq = request{conn: c, p: p}
	c.readDone = false
	c.readN, c.readErr = 0, nil
	select {
	case c.l.reqCh <- &c.readReq:
		c.l.wake()
	case <-c.l.done:
		c.readMu.Unlock()
		return 0, net.ErrClosed
	}
	for !c.readDone {
		c.readCond.Wait()
	}
	n, err := c.readN, c.readErr
	c.readDone = false
	c.readMu.Unlock()
	return n, err
}

// Write implements net.Conn.
func (c *Conn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	c.writeMu.Lock()
	c.writeReq = request{conn: c, p: p, write: true}
	c.writeDone = false
	c.writeN, c.writeErr = 0, nil
	select {
	case c.l.reqCh <- &c.writeReq:
		c.l.wake()
	case <-c.l.done:
		c.writeMu.Unlock()
		return 0, net.ErrClosed
	}
	for !c.writeDone {
		c.writeCond.Wait()
	}
	n, err := c.writeN, c.writeErr
	c.writeDone = false
	c.writeMu.Unlock()
	return n, err
}

// Close implements net.Conn. Shutdown wakes a pending io_uring receive or send
// before the descriptor is closed.
func (c *Conn) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	c.deadlineMu.Lock()
	if c.readTimer != nil {
		c.readTimer.Stop()
		c.readTimer = nil
	}
	if c.writeTimer != nil {
		c.writeTimer.Stop()
		c.writeTimer = nil
	}
	c.deadlineMu.Unlock()
	if c.readID.Load() != 0 || c.writeID.Load() != 0 {
		_ = unix.Shutdown(c.fd, unix.SHUT_RDWR)
	}
	err := unix.Close(c.fd)
	c.l.dropConn(c)
	return err
}

func (l *Listener) dropConn(c *Conn) {
	l.connMu.Lock()
	delete(l.conns, c)
	l.connMu.Unlock()
	l.maybeStop()
}

// SyscallConn exposes the socket descriptor to net/http hijack and encryl
// attach paths. The descriptor is owned by the connection.
func (c *Conn) SyscallConn() (syscall.RawConn, error) {
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	return &rawConn{c: c}, nil
}

type rawConn struct {
	c *Conn
}

func (r *rawConn) Control(f func(fd uintptr)) error {
	if r.c.closed.Load() {
		return net.ErrClosed
	}
	f(uintptr(r.c.fd))
	return nil
}

func (r *rawConn) Read(f func(fd uintptr) (done bool)) error {
	return errors.New("httpuring: raw read is not supported")
}

func (r *rawConn) Write(f func(fd uintptr) (done bool)) error {
	return errors.New("httpuring: raw write is not supported")
}

// LocalAddr implements net.Conn.
func (c *Conn) LocalAddr() net.Addr { return c.local }

// RemoteAddr implements net.Conn.
func (c *Conn) RemoteAddr() net.Addr { return c.remote }

// SetDeadline implements net.Conn.
func (c *Conn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

// SetReadDeadline implements net.Conn. A deadline cancels the pending io_uring
// receive so net/http's background-read abort path can make progress without
// closing a healthy keep-alive connection.
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	if c.readTimer != nil {
		c.readTimer.Stop()
		c.readTimer = nil
	}
	c.readExpired.Store(false)
	if t.IsZero() || c.closed.Load() {
		return nil
	}
	d := time.Until(t)
	if d <= 0 {
		c.readExpired.Store(true)
		if id := c.readID.Load(); id != 0 {
			c.cancel(id)
		}
		return nil
	}
	c.readTimer = time.AfterFunc(d, func() {
		c.readExpired.Store(true)
		if id := c.readID.Load(); id != 0 {
			c.cancel(id)
		}
	})
	return nil
}

// SetWriteDeadline implements net.Conn.
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	if c.writeTimer != nil {
		c.writeTimer.Stop()
		c.writeTimer = nil
	}
	c.writeExpired.Store(false)
	if t.IsZero() || c.closed.Load() {
		return nil
	}
	d := time.Until(t)
	if d <= 0 {
		c.writeExpired.Store(true)
		if id := c.writeID.Load(); id != 0 {
			c.cancel(id)
		}
		return nil
	}
	c.writeTimer = time.AfterFunc(d, func() {
		c.writeExpired.Store(true)
		if id := c.writeID.Load(); id != 0 {
			c.cancel(id)
		}
	})
	return nil
}

func (c *Conn) cancel(id uint64) {
	r := &request{conn: c, cancel: true, cancelID: id}
	select {
	case c.l.reqCh <- r:
		c.l.wake()
	case <-c.l.done:
	}
}

func peerAddr(fd int) net.Addr {
	sa, err := unix.Getpeername(fd)
	if err != nil {
		return nil
	}
	switch s := sa.(type) {
	case *unix.SockaddrInet4:
		ip := make(net.IP, 4)
		copy(ip, s.Addr[:])
		return &net.TCPAddr{IP: ip, Port: s.Port}
	case *unix.SockaddrInet6:
		ip := make(net.IP, 16)
		copy(ip, s.Addr[:])
		zone := ""
		if s.ZoneId != 0 {
			if iface, err := net.InterfaceByIndex(int(s.ZoneId)); err == nil {
				zone = iface.Name
			}
		}
		return &net.TCPAddr{IP: ip, Port: s.Port, Zone: zone}
	default:
		return nil
	}
}
