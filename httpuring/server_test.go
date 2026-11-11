//go:build linux

package httpuring

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/netio"
	"github.com/encryptedbusiness/encryl/uring"
)

func TestRawServer(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/json", func(req *Request, res *Response) {
		res.ContentType = benchmarkJSONType
		res.Body = benchmarkJSONBody
	})
	mux.HandleFunc("/echo", func(req *Request, res *Response) {
		res.Body = req.Body
	})

	ln, err := netioListenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Handler: mux, ServerHeader: "httpuring"}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	defer func() {
		_ = srv.Close()
		<-errCh
	}()

	base := "http://" + ln.Addr().String()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || string(body) != `{"message":"Hello, World!"}` {
		t.Fatalf("json = %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Server") != "httpuring" || resp.Header.Get("Content-Length") != "27" {
		t.Fatalf("headers = %v", resp.Header)
	}

	resp, err = client.Post(base+"/echo", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "hello" {
		t.Fatalf("echo = %q, %v", body, err)
	}

	resp, err = client.Get(base + "/missing")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("missing = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestRawServerPipeline(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/plaintext", func(req *Request, res *Response) {
		res.Body = []byte("Hello, World!")
	})
	ln, err := netioListenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Handler: mux}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	defer func() {
		_ = srv.Close()
		<-errCh
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req := "GET /plaintext HTTP/1.1\r\nHost: x\r\n\r\n"
	if _, err := io.WriteString(conn, req+req+req); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	for i := 0; i < 3; i++ {
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || string(body) != "Hello, World!" {
			t.Fatalf("pipeline %d = %q, %v", i, body, err)
		}
	}
}

func netioListenTest(t *testing.T) (netio.Listener, error) {
	t.Helper()
	return listenTest("127.0.0.1:0")
}

func listenTest(addr string) (netio.Listener, error) {
	return netio.Listen(addr, false, 4096)
}

var (
	benchmarkJSONType = []byte("application/json")
	benchmarkJSONBody = []byte(`{"message":"Hello, World!"}`)
)

func BenchmarkParseJSON(b *testing.B) {
	mux := &Mux{}
	mux.HandleFunc("/json", func(req *Request, res *Response) {
		res.ContentType = benchmarkJSONType
		res.Body = benchmarkJSONBody
	})
	rx, err := uring.NewRXArena(fastInSize, 1)
	if err != nil {
		b.Fatal(err)
	}
	defer rx.Close()
	tx, err := uring.NewTXArena(fastInSize, fastInSize)
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Close()
	f := &fastLoop{server: &Server{Handler: mux}, date: make([]byte, 0, 32), rx: rx, tx: tx}
	bid, ok := rx.Take()
	if !ok {
		b.Fatal("rx buffer")
	}
	c := &fastConn{fd: -1, id: 1, gen: 1, flags: flagLive, rxBid: bid, txBid: noBid, hdrEnd: -1}
	req := []byte("GET /json HTTP/1.1\r\nHost: server\r\nConnection: keep-alive\r\n\r\n")
	copy(rx.Bytes(bid), req)
	c.rxEnd = int32(len(req))
	if _, need, ok := f.parse(c); need || !ok {
		b.Fatal(need, ok)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(rx.Bytes(bid), req)
		c.rxStart, c.rxEnd = 0, int32(len(req))
		c.flags = flagLive
		c.txOff, c.txLen = 0, 0
		c.txHeap = nil
		c.scanOff = 0
		c.hdrEnd = -1
		c.hdrLen = 0
		c.hdrBits = 0
		if c.txBid >= 0 {
			tx.Release(c.txBid, c.id, c.gen)
			c.txBid = noBid
		}
		if _, need, ok := f.parse(c); need || !ok {
			b.Fatal(need, ok)
		}
	}
}

func TestHTTPServerRing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"Hello, World!"}`))
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "user" || pass != "pass" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	ln, err := netioListenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{HTTPHandler: mux, Workers: 1, ServerHeader: "httpuring"}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	defer func() {
		_ = srv.Close()
		<-errCh
	}()

	base := "http://" + ln.Addr().String()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != `{"message":"Hello, World!"}` {
		t.Fatalf("json = %q, %v", body, err)
	}
	resp, err = client.Post(base+"/echo", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "hello" {
		t.Fatalf("echo = %q, %v", body, err)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/auth", nil)
	req.SetBasicAuth("user", "pass")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("auth = %d %q", resp.StatusCode, body)
	}
}

func TestRawServerHeaderTimeout(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/json", func(req *Request, res *Response) {
		res.Body = []byte("ok")
	})
	ln, err := netioListenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Handler: mux, HeaderTimeout: 40 * time.Millisecond}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	defer func() {
		_ = srv.Close()
		<-errCh
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET /json HTTP/1.1\r\nHost: x\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var buf [1]byte
	if _, err := conn.Read(buf[:]); err == nil {
		t.Fatal("slow header connection was not closed")
	}
}

func TestRawServerIdleTimeout(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/json", func(req *Request, res *Response) {
		res.Body = []byte("ok")
	})
	ln, err := netioListenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Handler: mux, HeaderTimeout: time.Second, IdleTimeout: 40 * time.Millisecond}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	defer func() {
		_ = srv.Close()
		<-errCh
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET /json HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	time.Sleep(100 * time.Millisecond)
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var buf [1]byte
	if _, err := conn.Read(buf[:]); err == nil {
		t.Fatal("idle connection was not closed")
	}
}

func TestRawServerMaxConnections(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/json", func(req *Request, res *Response) { res.Body = []byte("ok") })
	ln, err := netioListenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Handler: mux, MaxConnections: 1, HeaderTimeout: time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	defer func() { _ = srv.Close(); <-errCh }()

	c1, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	if _, err := io.WriteString(c1, "GET /json HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	_ = c2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var b [1]byte
	if _, err := c2.Read(b[:]); err == nil {
		t.Fatal("second connection was not rejected")
	}
}
