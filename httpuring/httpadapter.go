//go:build linux

package httpuring

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type httpJob struct {
	c    *fastConn
	gen  uint32
	loop *fastLoop

	req        http.Request
	url        url.URL
	reqHeader  http.Header
	respHeader http.Header
	body       []byte
	bodyRead   bodyReader
	rw         httpResponseWriter
	status     int
	closeConn  bool
}

type bodyReader struct {
	data []byte
	off  int
}

func (r *bodyReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

func (r *bodyReader) Close() error { return nil }

type httpResponseWriter struct {
	job *httpJob
}

func (w *httpResponseWriter) Header() http.Header { return w.job.respHeader }

func (w *httpResponseWriter) WriteHeader(code int) {
	if w.job.status == 0 {
		w.job.status = code
	}
}

func (w *httpResponseWriter) Write(p []byte) (int, error) {
	if w.job.status == 0 {
		w.job.status = http.StatusOK
	}
	w.job.body = append(w.job.body, p...)
	return len(p), nil
}

func (w *httpResponseWriter) Flush() {}

// Hijack hands the raw ring-owned descriptor to a protocol upgrade such as
// encryl's WebSocket handoff. It is only available in inline mode; the
// worker ring owns the loop thread and cannot safely detach the descriptor
// from a worker goroutine.
func (w *httpResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	j := w.job
	if j == nil || j.c == nil || j.loop == nil || j.c.gen != j.gen || j.c.has(flagHijacked) {
		return nil, nil, errors.New("httpuring: connection already hijacked")
	}
	c := j.c
	f := j.loop
	if f.reqCh != nil {
		return nil, nil, errors.New("httpuring: hijack requires Workers=0")
	}
	if c.has(flagRecv | flagSend) {
		// The ring still references a buffer for this connection; detaching
		// it now would let the buffer be reused while the kernel owns it.
		return nil, nil, errors.New("httpuring: hijack with I/O in flight")
	}
	var extra []byte
	if c.rxBid >= 0 && c.extra != nil && f.rx != nil {
		buf := f.rx.Bytes(c.rxBid)
		lo := int(c.rxStart) + c.extra.httpConsumed
		hi := int(c.rxEnd)
		if lo >= 0 && hi <= len(buf) && hi > lo {
			extra = make([]byte, hi-lo)
			copy(extra, buf[lo:hi])
		}
	}
	fd := int(c.fd)
	remote := connAddr(c)
	local := f.ln.Addr()
	f.disarmAll(c)
	c.fd = -1
	c.set(flagHijacked)
	c.clear(flagHTTPPending)
	f.releaseRX(c)
	f.releaseTX(c)
	f.slots.release(c.id)
	hc := &hijackConn{fd: fd, local: local, remote: remote}
	br := bufio.NewReaderSize(bytes.NewReader(extra), len(extra))
	if len(extra) > 0 {
		if _, err := br.Peek(len(extra)); err != nil {
			_ = hc.Close()
			return nil, nil, err
		}
	}
	bw := bufio.NewWriter(io.Discard)
	return hc, bufio.NewReadWriter(br, bw), nil
}

func connAddr(c *fastConn) net.Addr {
	if c == nil || c.addrLen == 0 {
		return nil
	}
	ip := make(net.IP, c.addrLen)
	copy(ip, c.addr[:c.addrLen])
	return &net.TCPAddr{IP: ip, Port: int(c.port)}
}

var (
	_ net.Conn = (*hijackConn)(nil)
)

type hijackConn struct {
	fd     int
	local  net.Addr
	remote net.Addr
	closed atomic.Bool
}

func (c *hijackConn) Read([]byte) (int, error)  { return 0, io.ErrClosedPipe }
func (c *hijackConn) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (c *hijackConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return unix.Close(c.fd)
}
func (c *hijackConn) LocalAddr() net.Addr              { return c.local }
func (c *hijackConn) RemoteAddr() net.Addr             { return c.remote }
func (c *hijackConn) SetDeadline(time.Time) error      { return nil }
func (c *hijackConn) SetReadDeadline(time.Time) error  { return nil }
func (c *hijackConn) SetWriteDeadline(time.Time) error { return nil }

func (c *hijackConn) SyscallConn() (syscall.RawConn, error) {
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	return &hijackRawConn{fd: c.fd}, nil
}

type hijackRawConn struct{ fd int }

func (r *hijackRawConn) Control(f func(uintptr)) error {
	f(uintptr(r.fd))
	return nil
}
func (r *hijackRawConn) Read(func(uintptr) (done bool)) error  { return nil }
func (r *hijackRawConn) Write(func(uintptr) (done bool)) error { return nil }

type httpJobPool struct {
	pool sync.Pool
}

func newHTTPJobPool() *httpJobPool {
	return &httpJobPool{pool: sync.Pool{New: func() any {
		return &httpJob{
			reqHeader:  make(http.Header),
			respHeader: make(http.Header),
		}
	}}}
}

func (p *httpJobPool) get() *httpJob {
	return p.pool.Get().(*httpJob)
}

func (p *httpJobPool) put(j *httpJob) {
	j.c = nil
	j.gen = 0
	j.loop = nil
	j.status = 0
	j.closeConn = false
	if cap(j.body) > fastInSize {
		j.body = nil
	} else {
		j.body = j.body[:0]
	}
	j.bodyRead.data = nil
	j.bodyRead.off = 0
	clear(j.reqHeader)
	clear(j.respHeader)
	j.req = http.Request{}
	j.url = url.URL{}
	j.rw.job = j
	p.pool.Put(j)
}

// prepare builds a pooled net/http request. The caller owns the connection's
// receive buffer while the job is in flight.
func (j *httpJob) prepare(c *fastConn, method, target, path, query, proto []byte, header []byte, body []byte, contentLength int, closeConn bool) {
	j.c = c
	j.closeConn = closeConn
	j.bodyRead.data = body
	j.bodyRead.off = 0

	j.url = url.URL{}
	if len(query) > 0 {
		j.url.RawQuery = string(query)
	}
	rawPath := string(path)
	if decoded, err := url.PathUnescape(rawPath); err == nil {
		j.url.Path = decoded
		if decoded != rawPath {
			j.url.RawPath = rawPath
		}
	} else {
		j.url.Path = rawPath
	}

	major, minor := protoVersion(proto)
	j.req = http.Request{
		Method:        string(method),
		URL:           &j.url,
		Proto:         string(proto),
		ProtoMajor:    major,
		ProtoMinor:    minor,
		Header:        j.reqHeader,
		Body:          http.NoBody,
		ContentLength: int64(contentLength),
		Host:          string(headerHost(header)),
		RequestURI:    string(target),
		RemoteAddr:    peerString(c),
		Close:         closeConn,
	}
	if contentLength > 0 {
		j.req.Body = &j.bodyRead
	}
	clear(j.reqHeader)
	parseHeaderMap(j.reqHeader, header)
	if host := j.reqHeader.Get("Host"); host != "" {
		j.req.Host = host
	}
	j.reqHeader.Del("Host")

	j.status = 0
	j.rw.job = j
	clear(j.respHeader)
}

func (j *httpJob) resetResponse() {
	j.status = 0
	j.body = j.body[:0]
	clear(j.respHeader)
}

func protoVersion(proto []byte) (int, int) {
	if len(proto) != 8 || proto[6] != '.' {
		return 1, 1
	}
	return int(proto[5] - '0'), int(proto[7] - '0')
}

func headerHost(header []byte) []byte {
	for len(header) > 0 {
		line := header
		i := indexCRLF(header)
		if i >= 0 {
			line = header[:i]
			header = header[i+2:]
		} else {
			header = nil
		}
		colon := indexByte(line, ':')
		if colon <= 0 {
			continue
		}
		if equalFold(line[:colon], "Host") {
			return trimSpace(line[colon+1:])
		}
	}
	return nil
}

func parseHeaderMap(dst http.Header, header []byte) {
	for len(header) > 0 {
		line := header
		i := indexCRLF(header)
		if i >= 0 {
			line = header[:i]
			header = header[i+2:]
		} else {
			header = nil
		}
		colon := indexByte(line, ':')
		if colon <= 0 {
			continue
		}
		name := string(line[:colon])
		value := string(trimSpace(line[colon+1:]))
		dst.Add(name, value)
	}
}

func indexCRLF(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' {
			return i
		}
	}
	return -1
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

func (f *fastLoop) dispatchHTTP(c *fastConn, method, target, path, query, proto, header, body []byte, contentLength int, closeConn bool) {
	job := f.jobs.get()
	job.loop = f
	job.gen = c.gen
	job.prepare(c, method, target, path, query, proto, header, body, contentLength, closeConn)
	if c.extra == nil {
		c.extra = &httpExtra{}
	}
	c.extra.job = job
	c.set(flagHTTPPending)
	if f.reqCh == nil {
		job.resetResponse()
		func() {
			defer func() {
				if recover() != nil {
					job.status = http.StatusInternalServerError
					job.body = job.body[:0]
				}
			}()
			f.server.HTTPHandler.ServeHTTP(&job.rw, &job.req)
		}()
		f.completeHTTP(job)
		return
	}
	select {
	case f.reqCh <- job:
	default:
		c.clear(flagHTTPPending)
		c.extra.job = nil
		f.dropExtra(c)
		f.jobs.put(job)
		f.closeConn(c)
	}
}

func (f *fastLoop) worker() {
	defer f.workers.Done()
	for {
		select {
		case <-f.stopCh:
			return
		case job := <-f.reqCh:
			job.resetResponse()
			func() {
				defer func() {
					if recover() != nil {
						job.status = http.StatusInternalServerError
						job.body = job.body[:0]
					}
				}()
				f.server.HTTPHandler.ServeHTTP(&job.rw, &job.req)
			}()
			select {
			case f.respCh <- job:
				f.wake()
			case <-f.stopCh:
				f.jobs.put(job)
				return
			}
		}
	}
}

func (f *fastLoop) drainResponses() {
	for {
		select {
		case job := <-f.respCh:
			f.completeHTTP(job)
		default:
			return
		}
	}
}

func (f *fastLoop) completeHTTP(job *httpJob) {
	c := job.c
	if c == nil || c.gen != job.gen || c.extra == nil || c.extra.job != job {
		f.jobs.put(job)
		return
	}
	c.extra.job = nil
	c.clear(flagHTTPPending)
	if !c.has(flagLive) || c.has(flagHijacked) {
		gen := job.gen
		f.releaseRX(c)
		f.jobs.put(job)
		f.tryRelease(c, gen)
		return
	}

	status := job.status
	if status == 0 {
		status = http.StatusOK
	}
	closeConn := job.closeConn || job.req.Close || headerClose(job.respHeader)
	f.build = f.build[:0]
	f.build = appendHTTPResponse(f.build, job, status, closeConn, f.serverHeader(), f.dateHeader())
	if closeConn {
		c.set(flagCloseAfter)
	}
	f.jobs.put(job)
	gen := c.gen
	f.beginSend(c, f.build)
	f.trimBuild()
	if c.gen == gen {
		f.dropExtra(c)
	}
}

func headerClose(h http.Header) bool {
	for _, value := range h.Values("Connection") {
		for _, tok := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "close") {
				return true
			}
		}
	}
	return false
}

func appendHTTPResponse(dst []byte, job *httpJob, status int, closeConn bool, serverHeader string, date []byte) []byte {
	if status < 100 || status > 999 {
		status = http.StatusInternalServerError
	}
	head := job.req.Method == http.MethodHead
	noBody := head || status < 200 || status == http.StatusNoContent || status == http.StatusNotModified
	dst = append(dst, httpPrefix...)
	dst = strconv.AppendInt(dst, int64(status), 10)
	dst = append(dst, ' ')
	dst = append(dst, statusText(status)...)
	dst = append(dst, serverPrefix...)
	if serverHeader == "" {
		serverHeader = "httpuring"
	}
	dst = appendSanitizedString(dst, serverHeader)
	dst = append(dst, datePrefix...)
	dst = append(dst, date...)

	for key, values := range job.respHeader {
		switch {
		case strings.EqualFold(key, "Content-Length"),
			strings.EqualFold(key, "Connection"),
			strings.EqualFold(key, "Transfer-Encoding"),
			strings.EqualFold(key, "Trailer"),
			strings.EqualFold(key, "Upgrade"),
			strings.EqualFold(key, "Keep-Alive"),
			strings.EqualFold(key, "Proxy-Connection"),
			strings.EqualFold(key, "Date"),
			strings.EqualFold(key, "Server"):
			continue
		}
		if !validHeaderName(key) {
			continue
		}
		for _, value := range values {
			if !validHeaderValue(value) {
				continue
			}
			dst = appendString(dst, "\r\n")
			dst = appendString(dst, key)
			dst = appendString(dst, ": ")
			dst = appendString(dst, value)
		}
	}
	if status >= 200 && status != http.StatusNoContent {
		dst = append(dst, contentLengthPrefix...)
		dst = strconv.AppendInt(dst, int64(len(job.body)), 10)
	}
	if closeConn {
		dst = append(dst, closePrefix...)
	}
	dst = append(dst, crlf...)
	if !noBody {
		dst = append(dst, job.body...)
	}
	return dst
}
