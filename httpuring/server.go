//go:build linux

package httpuring

import (
	"bytes"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/encryptedbusiness/encryl/netio"
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
	fastInSize  = 8 << 10
	fastOutSize = 8 << 10
	httpDateFmt = "Mon, 02 Jan 2006 15:04:05 GMT"
)

func fastUserData(op uint8, fd int) uint64 {
	return uint64(op)<<56 | uint64(uint32(fd))
}

func fastDecode(ud uint64) (uint8, int) {
	return uint8(ud >> 56), int(uint32(ud))
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
	HeaderTimeout  time.Duration
	IdleTimeout    time.Duration
	WriteTimeout   time.Duration
	MaxOutputBytes int
	ServerHeader   string

	// OnRequest is called on the ring loop after a response has been fully
	// written. It is optional and intended for benchmarking/tracing.
	OnRequest func(time.Duration)

	mu      sync.Mutex
	loops   []*fastLoop
	listens []netio.Listener
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
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ring, err := uring.NewRing(4096)
	if err != nil {
		return err
	}
	defer ring.QueueExit()

	f := &fastLoop{
		server:  s,
		ln:      ln,
		lnFD:    ln.FD(),
		ring:    ring,
		conns:   make(map[int]*fastConn),
		closeCh: make(chan *fastConn, 1024),
	}
	defer f.closeAll()
	if s.HTTPHandler != nil {
		f.jobs = newHTTPJobPool()
		if s.Workers > 0 {
			f.reqCh = make(chan *httpJob, 1024)
			f.respCh = make(chan *httpJob, 1024)
			for i := 0; i < s.Workers; i++ {
				go f.worker()
			}
			defer close(f.reqCh)
		}
	}
	wakeFD, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		return err
	}
	f.wakeFD = wakeFD
	defer func() { _ = unix.Close(wakeFD) }()
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
	for {
		if f.stop.Load() {
			return nil
		}
		if _, err := ring.SubmitAndWait(1); err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		f.processCQEs()
		if f.stop.Load() {
			return nil
		}
	}
}

type fastLoop struct {
	server *Server
	ln     netio.Listener
	lnFD   int
	ring   *giouring.Ring
	conns  map[int]*fastConn

	acceptPending int
	stop          atomic.Bool
	wakeFD        int
	wakeArmed     bool
	dateUnix      int64
	date          []byte
	dateCount     uint32
	closeCh       chan *fastConn

	jobs   *httpJobPool
	reqCh  chan *httpJob
	respCh chan *httpJob
}

type fastConn struct {
	fd     int
	in     []byte
	start  int
	end    int
	out    []byte
	outOff int

	recv         bool
	poll         bool
	send         bool
	closeAfter   bool
	remote       string
	req          Request
	res          Response
	body         [512]byte
	httpJob      *httpJob
	httpPending  bool
	httpConsumed int
	hijacked     bool
	loop         *fastLoop
	reqStart     int64
	timerMu      sync.Mutex
	headerTimer  *time.Timer
	idleTimer    *time.Timer
	writeTimer   *time.Timer
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
	sqe.UserData = fastUserData(rawOpWake, 0)
	f.wakeArmed = true
	return true
}

func (f *fastLoop) getSQE() *giouring.SubmissionQueueEntry {
	if sqe := f.ring.GetSQE(); sqe != nil {
		return sqe
	}
	if _, err := f.ring.Submit(); err != nil {
		return nil
	}
	return f.ring.GetSQE()
}

const fastAcceptBatch = 64

func (f *fastLoop) submitAccept() {
	for f.acceptPending < fastAcceptBatch && !f.stop.Load() {
		sqe := f.getSQE()
		if sqe == nil {
			return
		}
		sqe.PrepareAccept(f.lnFD, 0, 0, unix.SOCK_CLOEXEC)
		sqe.UserData = fastUserData(rawOpAccept, 0)
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
	op, fd := fastDecode(cqe.UserData)
	switch op {
	case rawOpAccept:
		if f.acceptPending > 0 {
			f.acceptPending--
		}
		if cqe.Res < 0 {
			f.stop.Store(true)
			return
		}
		fd := int(cqe.Res)
		_ = netio.TuneAcceptedFD(fd)
		_ = unix.SetNonblock(fd, true)
		c := &fastConn{
			fd:     fd,
			remote: peerAddrString(fd),
			in:     make([]byte, fastInSize),
			out:    make([]byte, 0, fastOutSize),
			loop:   f,
		}
		if f.server.MaxConnections > 0 && len(f.conns) >= f.server.MaxConnections {
			_ = unix.Close(fd)
			f.submitAccept()
			return
		}
		f.conns[c.fd] = c
		f.submitAccept()
		f.startHeaderTimer(c)
		f.submitPoll(c)
	case rawOpPoll:
		f.handlePoll(fd, cqe.Res, cqe.Flags)
	case rawOpRecv:
		f.handleRecv(fd, cqe.Res)
	case rawOpSend:
		f.handleSend(fd, cqe.Res)
	case rawOpWake:
		f.wakeArmed = false
		var b [8]byte
		for {
			if _, err := unix.Read(f.wakeFD, b[:]); err != nil {
				break
			}
		}
		f.drainCloses()
		f.drainResponses()
		if !f.stop.Load() && !f.armWake() {
			f.stop.Store(true)
		}
	}
}

func peerAddrString(fd int) string {
	a := peerAddr(fd)
	if a == nil {
		return ""
	}
	return a.String()
}

func (f *fastLoop) drainCloses() {
	for {
		select {
		case c := <-f.closeCh:
			if c != nil && c.fd >= 0 && f.conns[c.fd] == c {
				f.close(c)
			}
		default:
			return
		}
	}
}

func (f *fastLoop) requestClose(c *fastConn) {
	select {
	case f.closeCh <- c:
		f.wake()
	default:
	}
}

func (f *fastLoop) startHeaderTimer(c *fastConn) {
	if f.server.HeaderTimeout <= 0 {
		return
	}
	c.timerMu.Lock()
	if c.headerTimer == nil {
		c.headerTimer = time.AfterFunc(f.server.HeaderTimeout, func() { f.requestClose(c) })
	} else {
		c.headerTimer.Reset(f.server.HeaderTimeout)
	}
	c.timerMu.Unlock()
}

func (f *fastLoop) stopHeaderTimer(c *fastConn) {
	c.timerMu.Lock()
	if c.headerTimer != nil {
		c.headerTimer.Stop()
	}
	c.timerMu.Unlock()
}

func (f *fastLoop) startIdleTimer(c *fastConn) {
	if f.server.IdleTimeout <= 0 {
		return
	}
	c.timerMu.Lock()
	if c.idleTimer == nil {
		c.idleTimer = time.AfterFunc(f.server.IdleTimeout, func() { f.requestClose(c) })
	} else {
		c.idleTimer.Reset(f.server.IdleTimeout)
	}
	c.timerMu.Unlock()
}

func (f *fastLoop) stopIdleTimer(c *fastConn) {
	c.timerMu.Lock()
	if c.idleTimer != nil {
		c.idleTimer.Stop()
	}
	c.timerMu.Unlock()
}

func (f *fastLoop) startWriteTimer(c *fastConn) {
	if f.server.WriteTimeout <= 0 {
		return
	}
	c.timerMu.Lock()
	if c.writeTimer == nil {
		c.writeTimer = time.AfterFunc(f.server.WriteTimeout, func() { f.requestClose(c) })
	} else {
		c.writeTimer.Reset(f.server.WriteTimeout)
	}
	c.timerMu.Unlock()
}

func (f *fastLoop) stopWriteTimer(c *fastConn) {
	c.timerMu.Lock()
	if c.writeTimer != nil {
		c.writeTimer.Stop()
	}
	c.timerMu.Unlock()
}

func (f *fastLoop) stopTimers(c *fastConn) {
	f.stopHeaderTimer(c)
	f.stopIdleTimer(c)
	f.stopWriteTimer(c)
}

func (f *fastLoop) submitPoll(c *fastConn) {
	if c.poll || c.recv || c.closeAfter || c.httpPending || c.hijacked {
		return
	}
	sqe := f.getSQE()
	if sqe == nil {
		f.close(c)
		return
	}
	sqe.PreparePollAdd(c.fd, unix.POLLIN)
	sqe.UserData = fastUserData(rawOpPoll, c.fd)
	c.poll = true
}

func (f *fastLoop) handlePoll(fd int, res int32, flags uint32) {
	c := f.conns[fd]
	if c == nil {
		return
	}
	c.poll = false
	if res < 0 {
		if res == -int32(unix.ECANCELED) {
			return
		}
		f.close(c)
		return
	}
	if res&int32(unix.POLLIN) != 0 {
		f.submitRecv(c)
		return
	}
	if res&int32(unix.POLLERR|unix.POLLHUP) != 0 {
		f.close(c)
		return
	}
	f.submitPoll(c)
}

func (f *fastLoop) submitRecv(c *fastConn) {
	if c.recv || c.closeAfter || c.httpPending || c.hijacked || len(c.out) >= f.server.MaxOutputBytes {
		return
	}
	if c.end == len(c.in) {
		if c.start > 0 {
			copy(c.in, c.in[c.start:c.end])
			c.end -= c.start
			c.start = 0
		}
		if c.end == len(c.in) {
			f.close(c)
			return
		}
	}
	sqe := f.getSQE()
	if sqe == nil {
		f.close(c)
		return
	}
	sqe.PrepareRecv(c.fd, uintptr(unsafe.Pointer(&c.in[c.end])), uint32(len(c.in)-c.end), 0)
	sqe.UserData = fastUserData(rawOpRecv, c.fd)
	c.recv = true
}

func (f *fastLoop) handleRecv(fd int, res int32) {
	c := f.conns[fd]
	if c == nil {
		return
	}
	c.recv = false
	if res == -int32(unix.EAGAIN) {
		f.submitPoll(c)
		return
	}
	if res <= 0 {
		f.close(c)
		return
	}
	f.stopIdleTimer(c)
	c.end += int(res)
	f.processRequests(c)
	if c.closeAfter || c.httpPending {
		return
	}
	if len(c.out) >= f.server.MaxOutputBytes {
		return
	}
	if !c.recv {
		f.submitPoll(c)
	}
}

func (f *fastLoop) processRequests(c *fastConn) {
	for {
		if len(c.out) >= f.server.MaxOutputBytes {
			return
		}
		consumed, need, ok := f.parse(c)
		if !ok {
			f.close(c)
			return
		}
		if need {
			f.startHeaderTimer(c)
			return
		}
		f.stopHeaderTimer(c)
		c.start += consumed
		if c.start == c.end {
			c.start, c.end = 0, 0
		}
		if c.httpPending || c.closeAfter {
			return
		}
	}
}

func (f *fastLoop) submitSend(c *fastConn) {
	if c.send || c.outOff >= len(c.out) {
		return
	}
	sqe := f.getSQE()
	if sqe == nil {
		f.close(c)
		return
	}
	sqe.PrepareSend(c.fd, uintptr(unsafe.Pointer(&c.out[c.outOff])), uint32(len(c.out)-c.outOff), unix.MSG_NOSIGNAL)
	sqe.UserData = fastUserData(rawOpSend, c.fd)
	c.send = true
	f.startWriteTimer(c)
}

func (f *fastLoop) handleSend(fd int, res int32) {
	c := f.conns[fd]
	if c == nil {
		return
	}
	c.send = false
	if res <= 0 {
		f.close(c)
		return
	}
	c.outOff += int(res)
	if c.outOff < len(c.out) {
		f.submitSend(c)
		return
	}
	c.out = c.out[:0]
	c.outOff = 0
	f.stopWriteTimer(c)
	if c.reqStart != 0 && f.server.OnRequest != nil {
		f.server.OnRequest(time.Duration(time.Now().UnixNano() - c.reqStart))
		c.reqStart = 0
	}
	if c.closeAfter {
		f.close(c)
		return
	}
	if !c.poll && !c.recv {
		f.submitPoll(c)
	}
}

func (f *fastLoop) close(c *fastConn) {
	if _, ok := f.conns[c.fd]; !ok {
		return
	}
	f.stopTimers(c)
	delete(f.conns, c.fd)
	_ = unix.Close(c.fd)
}

func (f *fastLoop) closeAll() {
	for _, c := range f.conns {
		f.stopTimers(c)
		_ = unix.Close(c.fd)
	}
	f.conns = make(map[int]*fastConn)
}

func (f *fastLoop) parse(c *fastConn) (consumed int, need bool, ok bool) {
	data := c.in[c.start:c.end]
	headerEnd := indexHeaderEnd(data)
	if headerEnd < 0 {
		if len(data) >= fastInSize-1 {
			return 0, false, false
		}
		return 0, true, true
	}
	lineEnd := bytes.IndexByte(data, '\r')
	if lineEnd < 0 || lineEnd > headerEnd {
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
	path := data[sp1+1 : sp2]
	proto := data[sp2+1 : lineEnd]
	if q := bytes.IndexByte(path, '?'); q >= 0 {
		path = path[:q]
	}
	if len(path) == 0 || path[0] != '/' {
		return 0, false, false
	}

	header := data[lineEnd+2 : headerEnd]
	var (
		contentLength int
		closeConn     bool
		keepAlive     bool
	)
	for len(header) > 0 {
		line := header
		if i := bytes.IndexByte(line, '\r'); i >= 0 {
			line = line[:i]
			if i+2 <= len(header) {
				header = header[i+2:]
			} else {
				header = nil
			}
		} else {
			header = nil
		}
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		name := line[:colon]
		value := bytes.TrimSpace(line[colon+1:])
		if equalFold(name, "Content-Length") {
			n, perr := parseDecimal(value)
			if !perr {
				return 0, false, false
			}
			contentLength = n
		} else if equalFold(name, "Connection") {
			if containsFold(value, "close") {
				closeConn = true
			}
			if containsFold(value, "keep-alive") {
				keepAlive = true
			}
		}
	}

	bodyStart := headerEnd + 4
	if len(data) < bodyStart+contentLength {
		return 0, true, true
	}
	body := data[bodyStart : bodyStart+contentLength]
	if f.server.OnRequest != nil {
		c.reqStart = time.Now().UnixNano()
	}
	if f.server.HTTPHandler != nil {
		c.httpConsumed = bodyStart + contentLength
		target := data[sp1+1 : sp2]
		query := target
		if q := bytes.IndexByte(target, '?'); q >= 0 {
			query = target[q+1:]
		} else {
			query = nil
		}
		f.dispatchHTTP(c, method, target, path, query, proto, data[lineEnd+2:headerEnd], body, contentLength, closeConn)
		return bodyStart + contentLength, false, true
	}

	req := &c.req
	*req = Request{
		Method: method,
		Path:   path,
		Proto:  proto,
		Header: data[lineEnd+2 : headerEnd],
		Body:   body,
	}
	res := &c.res
	*res = Response{Status: 200, Body: c.body[:0]}
	f.server.Handler.ServeHTTP(req, res)
	if res.Status == 0 {
		res.Status = 200
	}
	if res.ContentType == nil {
		res.ContentType = []byte("text/plain; charset=utf-8")
	}
	keep := !closeConn && (protoHTTP11(proto) || keepAlive)
	if res.Close {
		keep = false
	}
	f.writeResponse(c, res, keep)
	if !keep {
		c.closeAfter = true
	}
	return bodyStart + contentLength, false, true
}

func protoHTTP11(proto []byte) bool {
	return len(proto) == 8 && proto[0] == 'H' && proto[1] == 'T' && proto[2] == 'T' && proto[3] == 'P' &&
		proto[4] == '/' && proto[5] == '1' && proto[6] == '.' && proto[7] == '1'
}

func indexHeaderEnd(b []byte) int {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' && b[i+2] == '\r' && b[i+3] == '\n' {
			return i
		}
	}
	return -1
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

func containsFold(b []byte, s string) bool {
	if len(s) == 0 {
		return true
	}
	if len(b) < len(s) {
		return false
	}
	for i := 0; i+len(s) <= len(b); i++ {
		ok := true
		for j := 0; j < len(s); j++ {
			c := b[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			d := s[j]
			if d >= 'A' && d <= 'Z' {
				d += 'a' - 'A'
			}
			if c != d {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (f *fastLoop) writeResponse(c *fastConn, res *Response, keep bool) {
	code := res.Status
	c.out = append(c.out, httpPrefix...)
	c.out = strconv.AppendInt(c.out, int64(code), 10)
	c.out = append(c.out, ' ')
	c.out = append(c.out, statusText(code)...)
	c.out = append(c.out, serverPrefix...)
	serverHeader := f.server.ServerHeader
	if serverHeader == "" {
		serverHeader = "httpuring"
	}
	c.out = appendString(c.out, serverHeader)
	c.out = append(c.out, datePrefix...)
	c.out = append(c.out, f.dateHeader()...)
	c.out = append(c.out, contentTypePrefix...)
	c.out = append(c.out, res.ContentType...)
	c.out = append(c.out, contentLengthPrefix...)
	c.out = strconv.AppendInt(c.out, int64(len(res.Body)), 10)
	if !keep {
		c.out = append(c.out, closePrefix...)
	}
	c.out = append(c.out, crlf...)
	c.out = append(c.out, res.Body...)
	f.submitSend(c)
}

var (
	httpPrefix          = []byte("HTTP/1.1 ")
	serverPrefix        = []byte("\r\nServer: ")
	datePrefix          = []byte("\r\nDate: ")
	contentTypePrefix   = []byte("\r\nContent-Type: ")
	contentLengthPrefix = []byte("\r\nContent-Length: ")
	closePrefix         = []byte("\r\nConnection: close")
	crlf                = []byte("\r\n\r\n")
)

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
	default:
		return []byte("Status")
	}
}
