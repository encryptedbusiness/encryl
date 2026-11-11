//go:build linux

package httpuring

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/netio"
	"github.com/encryptedbusiness/encryl/uring"
	"golang.org/x/sys/unix"
)

// --- parser harness -------------------------------------------------------

func newParseHarness(t *testing.T, h Handler) (*fastLoop, *fastConn) {
	t.Helper()
	rx, err := uring.NewRXArena(fastInSize, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rx.Close() })
	tx, err := uring.NewTXArena(int64(fastInSize), fastInSize)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	f := &fastLoop{server: &Server{Handler: h, MaxOutputBytes: 256 << 10}, rx: rx, tx: tx}
	bid, ok := rx.Take()
	if !ok {
		t.Fatal("rx buffer")
	}
	c := &fastConn{fd: -1, id: 1, gen: 1, flags: flagLive, rxBid: bid, txBid: noBid}
	t.Cleanup(func() {
		if c.txBid >= 0 {
			tx.Release(c.txBid, c.id, c.gen)
		}
		rx.Put(bid)
	})
	return f, c
}

func resetParseConn(f *fastLoop, c *fastConn) {
	if c.txBid >= 0 && f.tx != nil {
		f.tx.Release(c.txBid, c.id, c.gen)
	}
	c.txBid = noBid
	c.txHeap = nil
	c.txOff, c.txLen = 0, 0
	c.extra = nil
	c.flags = flagLive
	c.rxStart, c.rxEnd = 0, 0
	c.scanOff = 0
	c.hdrEnd = -1
	c.hdrLen = 0
	c.hdrBits = 0
}

func feedParse(t *testing.T, f *fastLoop, c *fastConn, data []byte) (int, bool, bool) {
	t.Helper()
	resetParseConn(f, c)
	if len(data) > fastInSize {
		t.Fatalf("input %d > buffer %d", len(data), fastInSize)
	}
	buf := f.rx.Bytes(c.rxBid)
	copy(buf, data)
	c.rxEnd = int32(len(data))
	return f.parse(c)
}

func TestParseHeaderlessRequest(t *testing.T) {
	f, c := newParseHarness(t, &Mux{})
	req := []byte("GET / HTTP/1.1\r\n\r\n")
	consumed, need, ok := feedParse(t, f, c, req)
	if !ok || need || consumed != len(req) {
		t.Fatalf("consumed=%d need=%v ok=%v", consumed, need, ok)
	}
}

func TestParseHeaderlessWithBody(t *testing.T) {
	f, c := newParseHarness(t, &Mux{})
	req := []byte("POST / HTTP/1.1\r\nContent-Length: 3\r\n\r\nabc")
	consumed, need, ok := feedParse(t, f, c, req)
	if !ok || need || consumed != len(req) {
		t.Fatalf("consumed=%d need=%v ok=%v", consumed, need, ok)
	}
}

func TestParseRejectsSmugglingHeaders(t *testing.T) {
	cases := map[string]string{
		"chunked":            "POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
		"chunked-case":       "POST / HTTP/1.1\r\nHost: x\r\ntransfer-encoding: Chunked\r\n\r\n0\r\n\r\n",
		"duplicate-cl":       "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\nContent-Length: 5\r\n\r\nhello",
		"duplicate-host":     "GET / HTTP/1.1\r\nHost: a\r\nHost: b\r\n\r\n",
		"space-before-colon": "POST / HTTP/1.1\r\nHost: x\r\nContent-Length : 5\r\n\r\nhello",
		"obs-fold":           "POST / HTTP/1.1\r\nHost: x\r\nX-A: b\r\n c\r\n\r\n",
		"bad-length":         "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: +5\r\n\r\nhello",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			f, c := newParseHarness(t, &Mux{})
			_, need, ok := feedParse(t, f, c, []byte(raw))
			if ok || need {
				t.Fatalf("accepted %q (need=%v ok=%v)", name, need, ok)
			}
		})
	}
}

func TestParseFuzzNoPanic(t *testing.T) {
	f, c := newParseHarness(t, &Mux{})
	r := rand.New(rand.NewSource(1))
	prefixes := [][]byte{
		nil,
		[]byte("GET / HTTP/1.1\r\n"),
		[]byte("POST / HTTP/1.1\r\nContent-Length: 4\r\n\r\n"),
		[]byte("GET / HTTP/1.0\r\n\r\n"),
	}
	for i := 0; i < 20000; i++ {
		p := prefixes[r.Intn(len(prefixes))]
		n := r.Intn(256)
		buf := make([]byte, 0, len(p)+n)
		buf = append(buf, p...)
		for j := 0; j < n; j++ {
			buf = append(buf, byte(r.Intn(256)))
		}
		func() {
			defer func() {
				if v := recover(); v != nil {
					t.Fatalf("panic on input %q: %v", buf, v)
				}
			}()
			if len(buf) <= fastInSize {
				feedParse(t, f, c, buf)
			}
		}()
	}
}

// --- server harness -------------------------------------------------------

func startTestServer(t *testing.T, srv *Server) (netio.Listener, func()) {
	t.Helper()
	ln, err := netioListenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	ready := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			_ = srv.Close()
			_ = ln.Close()
			t.Fatalf("server did not start: %v", err)
		default:
		}
		srv.mu.Lock()
		ready = len(srv.loops) > 0
		srv.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ready {
		_ = srv.Close()
		_ = ln.Close()
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("server did not start")
			}
			t.Fatalf("server did not start: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("server did not start; Serve did not return after Close")
		}
	}
	cleanup := func() {
		_ = srv.Close()
		select {
		case <-errCh: //nolint:errcheck // wait for Serve to return after Close
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not return after Close")
		}
	}
	return ln, cleanup
}

func dialRead(t *testing.T, addr string, raw string) (int, string, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, raw); err != nil {
		return 0, "", err
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), nil
}

func TestHeaderlessRequestServedOverNetwork(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/ok", func(req *Request, res *Response) { res.Body = []byte("ok") })
	srv := &Server{Handler: mux, HeaderTimeout: 2 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	code, body, err := dialRead(t, ln.Addr().String(), "GET /ok HTTP/1.1\r\n\r\n")
	if err != nil || code != 200 || body != "ok" {
		t.Fatalf("headerless request: code=%d body=%q err=%v", code, body, err)
	}

	// The loop must still be alive for a normal request.
	code, body, err = dialRead(t, ln.Addr().String(), "GET /ok HTTP/1.1\r\nHost: x\r\n\r\n")
	if err != nil || code != 200 || body != "ok" {
		t.Fatalf("follow-up request: code=%d body=%q err=%v", code, body, err)
	}
}

func TestPipelineOrdering(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/echo", func(req *Request, res *Response) { res.Body = req.Body })
	srv := &Server{Handler: mux, HeaderTimeout: 2 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const n = 200
	var sb strings.Builder
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("body-%03d", i)
		fmt.Fprintf(&sb, "POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	}
	if _, err := io.WriteString(conn, sb.String()); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	for i := 0; i < n; i++ {
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("response %d: %v", i, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		want := fmt.Sprintf("body-%03d", i)
		if err != nil || string(body) != want {
			t.Fatalf("response %d = %q (%v), want %q", i, body, err, want)
		}
	}
}

func TestRXArenaExhaustionAndPark(t *testing.T) {
	raiseNoFile(t)
	const conns = fastArenaCount + 200
	mux := &Mux{}
	mux.HandleFunc("/ok", func(req *Request, res *Response) { res.Body = []byte("ok") })
	srv := &Server{Handler: mux, HeaderTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	clients := make([]net.Conn, 0, conns)
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	addr := ln.Addr().String()
	for i := 0; i < conns; i++ {
		c, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		// A partial request makes each connection hold a receive buffer.
		if _, err := io.WriteString(c, "GET /ok HTTP/1.1\r\nHost: x\r\n"); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		clients = append(clients, c)
	}
	// Complete every request; parked connections must be woken as buffers free.
	for i, c := range clients {
		if _, err := io.WriteString(c, "\r\n"); err != nil {
			t.Fatalf("complete %d: %v", i, err)
		}
	}
	for i, c := range clients {
		_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("response %d: %v", i, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || string(body) != "ok" {
			t.Fatalf("response %d = %q (%v)", i, body, err)
		}
	}
}

func TestKeepAliveChurnDoesNotLeakBuffers(t *testing.T) {
	raiseNoFile(t)
	const conns = 1200
	mux := &Mux{}
	mux.HandleFunc("/ok", func(req *Request, res *Response) { res.Body = []byte("ok") })
	srv := &Server{Handler: mux, HeaderTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()
	addr := ln.Addr().String()

	for round := 0; round < 2; round++ {
		clients := make([]net.Conn, 0, conns)
		for i := 0; i < conns; i++ {
			c, err := net.DialTimeout("tcp", addr, 3*time.Second)
			if err != nil {
				t.Fatalf("round %d dial %d: %v", round, i, err)
			}
			if _, err := io.WriteString(c, "GET /ok HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
				t.Fatalf("round %d write %d: %v", round, i, err)
			}
			clients = append(clients, c)
		}
		for i, c := range clients {
			_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
			br := bufio.NewReader(c)
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatalf("round %d response %d: %v", round, i, err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			_ = c.Close()
		}
	}
}

func TestInlinePanicReturns500AndKeepsLoopAlive(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) { panic("boom") })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	srv := &Server{HTTPHandler: mux, HeaderTimeout: 2 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	code, _, err := dialRead(t, ln.Addr().String(), "GET /boom HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if err != nil || code != 500 {
		t.Fatalf("panic request: code=%d err=%v", code, err)
	}
	code, body, err := dialRead(t, ln.Addr().String(), "GET /ok HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if err != nil || code != 200 || body != "ok" {
		t.Fatalf("follow-up after panic: code=%d body=%q err=%v", code, body, err)
	}
}

func TestResponseHeaderInjectionBlocked(t *testing.T) {
	t.Run("raw-content-type", func(t *testing.T) {
		mux := &Mux{}
		mux.HandleFunc("/x", func(req *Request, res *Response) {
			res.ContentType = []byte("text/plain\r\nX-Injected: yes")
			res.Body = []byte("ok")
		})
		srv := &Server{Handler: mux, HeaderTimeout: 2 * time.Second}
		ln, cleanup := startTestServer(t, srv)
		defer cleanup()

		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "GET /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("X-Injected"); got != "" {
			t.Fatalf("injected header present: %q", got)
		}
	})

	t.Run("http-handler-header", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/x", func(w http.ResponseWriter, r *http.Request) {
			w.Header()["X-Bad"] = []string{"v\r\nX-Injected: yes"}
			_, _ = w.Write([]byte("ok"))
		})
		srv := &Server{HTTPHandler: mux, HeaderTimeout: 2 * time.Second}
		ln, cleanup := startTestServer(t, srv)
		defer cleanup()

		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "GET /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("X-Injected"); got != "" {
			t.Fatalf("injected header present: %q", got)
		}
	})
}

func TestHijackHandsOffPipelinedBytes(t *testing.T) {
	got := make(chan string, 1)
	var once sync.Once
	report := func(s string) { once.Do(func() { got <- s }) }
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			report("no-hijacker")
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			report("hijack:" + err.Error())
			return
		}
		defer func() {
			// Close must be idempotent: a double close must not release an
			// unrelated descriptor.
			_ = conn.Close()
			_ = conn.Close()
		}()
		if conn.RemoteAddr() == nil || conn.LocalAddr() == nil {
			report("nil-addr")
			return
		}
		n := rw.Reader.Buffered()
		b := make([]byte, n)
		_, _ = io.ReadFull(rw.Reader, b)
		report(fmt.Sprintf("buffered=%d data=%s", n, b))
	})
	srv := &Server{HTTPHandler: h, HeaderTimeout: 2 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Send the upgrade request and the first post-upgrade bytes together.
	_, _ = io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nHELLO")
	select {
	case s := <-got:
		if s != "buffered=5 data=HELLO" {
			t.Fatalf("hijack handoff = %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hijack handler did not run")
	}
}

func TestWorkerModePipelineAndShutdown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	})
	srv := &Server{HTTPHandler: mux, Workers: 2, HeaderTimeout: 5 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const n = 50
	var sb strings.Builder
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("w-%03d", i)
		fmt.Fprintf(&sb, "POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	}
	if _, err := io.WriteString(conn, sb.String()); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	for i := 0; i < n; i++ {
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("response %d: %v", i, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		want := fmt.Sprintf("w-%03d", i)
		if err != nil || string(body) != want {
			t.Fatalf("response %d = %q (%v), want %q", i, body, err, want)
		}
	}
	// cleanup closes the server and must join workers and unmap arenas safely.
}

// --- outbound buffer state machine ---------------------------------------

func newTXLoop(t *testing.T, blockBytes, blocks int) (*fastLoop, *fastConn, *uring.TXArena) {
	t.Helper()
	tx, err := uring.NewTXArena(int64(blockBytes*blocks), blockBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[0]); _ = unix.Close(fds[1]) })
	f := &fastLoop{server: &Server{MaxOutputBytes: 1 << 20}, tx: tx}
	_, c := f.slots.alloc(fds[0])
	return f, c, tx
}

func TestTXExtendWithinBlockKeepsOrder(t *testing.T) {
	f, c, tx := newTXLoop(t, 64, 2)
	a := bytes.Repeat([]byte("a"), 40)
	b := bytes.Repeat([]byte("b"), 20)
	f.beginSend(c, a)
	if c.txBid < 0 || c.txLen != 40 {
		t.Fatalf("first send bid=%d len=%d", c.txBid, c.txLen)
	}
	f.beginSend(c, b)
	if c.txBid < 0 || c.txLen != 60 {
		t.Fatalf("extended send bid=%d len=%d", c.txBid, c.txLen)
	}
	got := tx.Bytes(c.txBid)[:c.txLen]
	want := append(append([]byte(nil), a...), b...)
	if !bytes.Equal(got, want) {
		t.Fatalf("extended block = %q, want %q", got, want)
	}
}

func TestTXOverflowQueuesPendingInOrder(t *testing.T) {
	f, c, tx := newTXLoop(t, 64, 2)
	a := bytes.Repeat([]byte("a"), 40)
	b := bytes.Repeat([]byte("b"), 40)
	f.beginSend(c, a)
	f.beginSend(c, b)
	if c.extra == nil || !bytes.Equal(c.extra.pending, b) {
		t.Fatalf("pending = %q, want %q", pendingOf(c), b)
	}
	// Complete the first send; the queued bytes must be handed to the arena.
	f.handleSend(c.id, c.gen, int32(len(a)))
	if c.txBid < 0 || c.txLen != int32(len(b)) {
		t.Fatalf("after completion bid=%d len=%d", c.txBid, c.txLen)
	}
	if !bytes.Equal(tx.Bytes(c.txBid)[:c.txLen], b) {
		t.Fatalf("pending block = %q, want %q", tx.Bytes(c.txBid)[:c.txLen], b)
	}
	if c.extra != nil {
		t.Fatalf("extra retained after drain: %+v", c.extra)
	}
}

func TestTXLargeResponseUsesHeapThenQueues(t *testing.T) {
	f, c, tx := newTXLoop(t, 64, 2)
	big := bytes.Repeat([]byte("x"), 100)
	small := bytes.Repeat([]byte("y"), 10)
	f.beginSend(c, big)
	if c.txHeap == nil || c.txLen != int32(len(big)) {
		t.Fatalf("heap send heap=%v len=%d", c.txHeap != nil, c.txLen)
	}
	f.beginSend(c, small)
	if c.extra == nil || !bytes.Equal(c.extra.pending, small) {
		t.Fatalf("pending = %q, want %q", pendingOf(c), small)
	}
	if len(c.txHeap) != len(big) {
		t.Fatalf("heap send mutated while in flight: %d", len(c.txHeap))
	}
	f.handleSend(c.id, c.gen, int32(len(big)))
	if c.txHeap != nil || c.txBid < 0 {
		t.Fatalf("after completion heap=%v bid=%d", c.txHeap != nil, c.txBid)
	}
	if !bytes.Equal(tx.Bytes(c.txBid)[:c.txLen], small) {
		t.Fatalf("queued block = %q, want %q", tx.Bytes(c.txBid)[:c.txLen], small)
	}
}

func pendingOf(c *fastConn) []byte {
	if c.extra == nil {
		return nil
	}
	return c.extra.pending
}
