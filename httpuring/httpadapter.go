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
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type httpJob struct {
	c *fastConn

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
	if j == nil || j.c == nil || j.c.hijacked {
		return nil, nil, errors.New("httpuring: connection already hijacked")
	}
	c := j.c
	if c.loop == nil || c.loop.reqCh != nil {
		return nil, nil, errors.New("httpuring: hijack requires Workers=0")
	}
	c.loop.stopTimers(c)
	extra := c.in[c.start+c.httpConsumed : c.end]
	c.hijacked = true
	delete(c.loop.conns, c.fd)
	fd := c.fd
	c.fd = -1
	hc := &hijackConn{fd: fd, local: c.loop.ln.Addr(), remote: c.remote}
	if len(extra) == 0 {
		return hc, emptyHandoffRW, nil
	}
	br := bufio.NewReader(bytes.NewReader(extra))
	bw := bufio.NewWriter(io.Discard)
	return hc, bufio.NewReadWriter(br, bw), nil
}

var emptyHandoffRW = bufio.NewReadWriter(
	bufio.NewReader(bytes.NewReader(nil)),
	bufio.NewWriter(io.Discard),
)

type hijackConn struct {
	fd     int
	local  net.Addr
	remote string
}

func (c *hijackConn) Read([]byte) (int, error)         { return 0, io.ErrClosedPipe }
func (c *hijackConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (c *hijackConn) Close() error                     { return unix.Close(c.fd) }
func (c *hijackConn) LocalAddr() net.Addr              { return c.local }
func (c *hijackConn) RemoteAddr() net.Addr             { return nil }
func (c *hijackConn) SetDeadline(time.Time) error      { return nil }
func (c *hijackConn) SetReadDeadline(time.Time) error  { return nil }
func (c *hijackConn) SetWriteDeadline(time.Time) error { return nil }

func (c *hijackConn) SyscallConn() (syscall.RawConn, error) {
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
	j.status = 0
	j.closeConn = false
	j.body = j.body[:0]
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
	j.url.Path = string(path)

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
		RemoteAddr:    c.remote,
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
	job.prepare(c, method, target, path, query, proto, header, body, contentLength, closeConn)
	if f.reqCh == nil {
		c.httpJob = job
		c.httpPending = true
		job.resetResponse()
		f.server.HTTPHandler.ServeHTTP(&job.rw, &job.req)
		f.completeHTTP(job)
		return
	}

	c.httpJob = job
	c.httpPending = true
	select {
	case f.reqCh <- job:
	default:
		c.httpPending = false
		c.httpJob = nil
		f.jobs.put(job)
		f.close(c)
	}
}

func (f *fastLoop) worker() {
	for job := range f.reqCh {
		job.resetResponse()
		func() {
			defer func() {
				if recover() != nil {
					job.status = http.StatusInternalServerError
				}
			}()
			f.server.HTTPHandler.ServeHTTP(&job.rw, &job.req)
		}()
		f.respCh <- job
		f.wake()
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
	if c == nil || c.httpJob != job {
		f.jobs.put(job)
		return
	}
	c.httpJob = nil
	c.httpPending = false
	if c.hijacked {
		f.jobs.put(job)
		return
	}

	status := job.status
	if status == 0 {
		status = http.StatusOK
	}
	closeConn := job.closeConn || job.req.Close || headerClose(job.respHeader)
	c.out = c.out[:0]
	writeHTTPResponse(c, job, status, closeConn, f.server.ServerHeader, f.dateHeader())
	c.closeAfter = closeConn
	f.jobs.put(job)
	f.submitSend(c)
}

func headerClose(h http.Header) bool {
	return strings.EqualFold(h.Get("Connection"), "close")
}

func writeHTTPResponse(c *fastConn, job *httpJob, status int, closeConn bool, serverHeader string, date []byte) {
	c.out = append(c.out, httpPrefix...)
	c.out = strconv.AppendInt(c.out, int64(status), 10)
	c.out = append(c.out, ' ')
	c.out = append(c.out, statusText(status)...)
	c.out = append(c.out, serverPrefix...)
	if serverHeader == "" {
		serverHeader = "httpuring"
	}
	c.out = appendString(c.out, serverHeader)
	c.out = append(c.out, datePrefix...)
	c.out = append(c.out, date...)

	for key, values := range job.respHeader {
		switch {
		case strings.EqualFold(key, "Content-Length"),
			strings.EqualFold(key, "Connection"),
			strings.EqualFold(key, "Transfer-Encoding"),
			strings.EqualFold(key, "Date"),
			strings.EqualFold(key, "Server"):
			continue
		}
		for _, value := range values {
			c.out = appendString(c.out, "\r\n")
			c.out = appendString(c.out, key)
			c.out = appendString(c.out, ": ")
			c.out = appendString(c.out, value)
		}
	}
	c.out = append(c.out, contentLengthPrefix...)
	c.out = strconv.AppendInt(c.out, int64(len(job.body)), 10)
	if closeConn {
		c.out = append(c.out, closePrefix...)
	}
	c.out = append(c.out, crlf...)
	c.out = append(c.out, job.body...)
}
