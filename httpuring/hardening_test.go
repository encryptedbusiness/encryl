//go:build linux

package httpuring

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseRejectsAdditionalSmuggling(t *testing.T) {
	cases := map[string]string{
		"bare-lf":              "POST / HTTP/1.1\r\nHost: x\nContent-Length: 5\r\n\r\nhello",
		"bare-cr":              "GET / HTTP/1.1\r\nHost: x\r\nX: y\rZ: w\r\n\r\n",
		"line-without-colon":   "GET / HTTP/1.1\r\nHost: x\r\nBroken\r\n\r\n",
		"space-in-name":        "GET / HTTP/1.1\r\nHost: x\r\nBad Header: v\r\n\r\n",
		"nul-in-value":         "GET / HTTP/1.1\r\nHost: x\r\nX: a\x00b\r\n\r\n",
		"ctl-in-value":         "GET / HTTP/1.1\r\nHost: x\r\nX: a\x01b\r\n\r\n",
		"bad-method":           "G@T / HTTP/1.1\r\nHost: x\r\n\r\n",
		"bad-proto":            "GET / HTTP/2.0\r\nHost: x\r\n\r\n",
		"ctl-in-target":        "GET /a\x01b HTTP/1.1\r\nHost: x\r\n\r\n",
		"obs-text-in-name":     "GET / HTTP/1.1\r\nHost: x\r\nX\x80: v\r\n\r\n",
		"empty-header-name":    "GET / HTTP/1.1\r\nHost: x\r\n: v\r\n\r\n",
		"space-before-colon-2": "GET / HTTP/1.1\r\nHost: x\r\nX\t: v\r\n\r\n",
		"bad-percent":          "GET /a%zz HTTP/1.1\r\nHost: x\r\n\r\n",
		"truncated-percent":    "GET /a%2 HTTP/1.1\r\nHost: x\r\n\r\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			f, c := newParseHarness(t, &Mux{})
			if _, need, ok := feedParse(t, f, c, []byte(raw)); ok || need {
				t.Fatalf("accepted %q (need=%v ok=%v)", name, need, ok)
			}
		})
	}
}

func TestParseConnectionTokenMatching(t *testing.T) {
	t.Run("substring-is-not-a-token", func(t *testing.T) {
		f, c := newParseHarness(t, &Mux{})
		raw := "GET / HTTP/1.1\r\nHost: x\r\nConnection: disclose\r\n\r\n"
		if _, need, ok := feedParse(t, f, c, []byte(raw)); !ok || need {
			t.Fatalf("need=%v ok=%v", need, ok)
		}
		if c.hdrBits&hdrClose != 0 {
			t.Fatal("substring 'close' inside 'disclose' matched")
		}
	})
	t.Run("list-token-matches", func(t *testing.T) {
		f, c := newParseHarness(t, &Mux{})
		raw := "GET / HTTP/1.1\r\nHost: x\r\nConnection: keep-alive, close\r\n\r\n"
		if _, need, ok := feedParse(t, f, c, []byte(raw)); !ok || need {
			t.Fatalf("need=%v ok=%v", need, ok)
		}
		if c.hdrBits&hdrClose == 0 || c.hdrBits&hdrKeepAlive == 0 {
			t.Fatalf("bits=%#x", c.hdrBits)
		}
	})
}

func TestParseIncrementalHeaderScan(t *testing.T) {
	f, c := newParseHarness(t, &Mux{})
	req := []byte("GET /inc HTTP/1.1\r\nHost: x\r\nX-Test: " + strings.Repeat("a", 512) + "\r\n\r\n")
	buf := f.rx.Bytes(c.rxBid)
	for i := 1; i <= len(req); i++ {
		copy(buf, req[:i])
		c.rxEnd = int32(i)
		consumed, need, ok := f.parse(c)
		if i < len(req) {
			if !ok || !need || consumed != 0 {
				t.Fatalf("prefix %d: consumed=%d need=%v ok=%v", i, consumed, need, ok)
			}
			if int(c.scanOff) != i {
				t.Fatalf("prefix %d rescanned: scanOff=%d", i, c.scanOff)
			}
			continue
		}
		if !ok || need || consumed != len(req) {
			t.Fatalf("complete: consumed=%d need=%v ok=%v", consumed, need, ok)
		}
	}
}

func TestParseOversizeBodyGets413(t *testing.T) {
	f, c := newParseHarness(t, &Mux{})
	raw := "POST /big HTTP/1.1\r\nHost: x\r\nContent-Length: 9000\r\n\r\n"
	_, need, ok := feedParse(t, f, c, []byte(raw))
	if !ok || !need {
		t.Fatalf("need=%v ok=%v", need, ok)
	}
	if !c.has(flagCloseAfter) {
		t.Fatal("oversized request was not marked close-after")
	}
	if c.txBid < 0 {
		t.Fatal("no 413 response queued")
	}
	got := f.tx.Bytes(c.txBid)[:c.txLen]
	if !bytes.HasPrefix(got, []byte("HTTP/1.1 413 ")) {
		t.Fatalf("response = %q", got)
	}
}

func TestParseExpectContinueSendsInterim(t *testing.T) {
	f, c := newParseHarness(t, &Mux{})
	raw := "POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\nExpect: 100-continue\r\n\r\n"
	_, need, ok := feedParse(t, f, c, []byte(raw))
	if !ok || !need {
		t.Fatalf("need=%v ok=%v", need, ok)
	}
	if c.txBid < 0 {
		t.Fatal("no interim response queued")
	}
	got := f.tx.Bytes(c.txBid)[:c.txLen]
	if !bytes.Equal(got, continueResponse) {
		t.Fatalf("interim = %q", got)
	}
	if c.hdrBits&hdrContinueSent == 0 {
		t.Fatal("continue-sent flag missing")
	}
}

func TestExpectContinueOverNetwork(t *testing.T) {
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
	if _, err := io.WriteString(conn, "POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\nExpect: 100-continue\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(conn)

	var interim strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("interim read: %v (%q)", err, interim.String())
		}
		interim.WriteString(line)
		if line == "\r\n" {
			break
		}
	}
	if !strings.HasPrefix(interim.String(), "HTTP/1.1 100 Continue") {
		t.Fatalf("interim response = %q", interim.String())
	}

	if _, err := io.WriteString(conn, "hello"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "hello" || resp.StatusCode != 200 {
		t.Fatalf("final = %d %q (%v)", resp.StatusCode, body, err)
	}
}

func TestOversizeBody413OverNetwork(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/x", func(req *Request, res *Response) { res.Body = []byte("ok") })
	srv := &Server{Handler: mux, HeaderTimeout: 2 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	code, body, err := dialRead(t, ln.Addr().String(),
		"POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 100000\r\n\r\n")
	if err != nil || code != 413 {
		t.Fatalf("code=%d body=%q err=%v", code, body, err)
	}
	if body != "" {
		t.Fatalf("413 body = %q", body)
	}
}

func TestBareLFRequestRejectedOverNetwork(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/ok", func(req *Request, res *Response) { res.Body = []byte("ok") })
	srv := &Server{Handler: mux, HeaderTimeout: time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /ok HTTP/1.1\r\nHost: x\nX: y\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [1]byte
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("bare-LF request was not rejected")
	}
	// The loop must survive the rejection.
	code, body, err := dialRead(t, ln.Addr().String(), "GET /ok HTTP/1.1\r\nHost: x\r\n\r\n")
	if err != nil || code != 200 || body != "ok" {
		t.Fatalf("follow-up: code=%d body=%q err=%v", code, body, err)
	}
}

func TestHeadResponseCarriesNoBody(t *testing.T) {
	rawMux := &Mux{}
	rawMux.HandleFunc("/plain", func(req *Request, res *Response) { res.Body = []byte("hello") })
	adapterMux := http.NewServeMux()
	adapterMux.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	})
	servers := map[string]*Server{
		"raw":     {Handler: rawMux, HeaderTimeout: 2 * time.Second},
		"adapter": {HTTPHandler: adapterMux, HeaderTimeout: 2 * time.Second},
	}
	for name, srv := range servers {
		t.Run(name, func(t *testing.T) {
			ln, cleanup := startTestServer(t, srv)
			defer cleanup()

			conn, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_, _ = io.WriteString(conn,
				"HEAD /plain HTTP/1.1\r\nHost: x\r\n\r\n"+
					"GET /plain HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			br := bufio.NewReader(conn)
			tp := textproto.NewReader(br)

			line, err := tp.ReadLine()
			if err != nil || line != "HTTP/1.1 200 OK" {
				t.Fatalf("head status = %q (%v)", line, err)
			}
			headHeaders, err := tp.ReadMIMEHeader()
			if err != nil {
				t.Fatal(err)
			}
			if got := headHeaders.Get("Content-Length"); got != "5" {
				t.Fatalf("HEAD content-length = %q", got)
			}
			// No body may follow a HEAD response: the next line must be the
			// pipelined GET response, not "hello".
			line, err = tp.ReadLine()
			if err != nil || line != "HTTP/1.1 200 OK" {
				t.Fatalf("next response after HEAD = %q (%v); body leaked into pipeline", line, err)
			}
			if _, err := tp.ReadMIMEHeader(); err != nil {
				t.Fatal(err)
			}
			body := make([]byte, 5)
			if _, err := io.ReadFull(br, body); err != nil || string(body) != "hello" {
				t.Fatalf("GET body = %q (%v)", body, err)
			}
		})
	}
}

func TestURLPathIsDecodedForAdapter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%s|%s", r.URL.Path, r.URL.RawQuery, r.URL.EscapedPath())
	})
	srv := &Server{HTTPHandler: mux, HeaderTimeout: 2 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	code, body, err := dialRead(t, ln.Addr().String(), "GET /a%2Fb?x=1%202 HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if err != nil || code != 200 {
		t.Fatalf("code=%d body=%q err=%v", code, body, err)
	}
	if body != "/a/b|x=1%202|/a%2Fb" {
		t.Fatalf("path/query = %q", body)
	}
}

func TestResponseStatusClampAndServerSanitization(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/x", func(req *Request, res *Response) {
		res.Status = 42
		res.Body = []byte("ok")
	})
	srv := &Server{Handler: mux, ServerHeader: "evil\r\nX-Injected: yes", HeaderTimeout: 2 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Injected"); got != "" {
		t.Fatalf("injected header = %q", got)
	}
	if got := resp.Header.Get("Server"); got != "evil" {
		t.Fatalf("server = %q", got)
	}
}

func TestAdapterHeaderFiltering(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/x", func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Bad Header"] = []string{"v"}
		w.Header()["X-Ctl"] = []string{"a\x01b"}
		w.Header()["X-Ok"] = []string{"fine"}
		w.Header()["Trailer"] = []string{"X-Late"}
		w.Header()["Upgrade"] = []string{"h2c"}
		w.WriteHeader(42)
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
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Ctl") != "" || resp.Header.Get("Bad Header") != "" {
		t.Fatalf("invalid headers forwarded: %v", resp.Header)
	}
	if resp.Header.Get("Trailer") != "" || resp.Header.Get("Upgrade") != "" {
		t.Fatalf("hop-by-hop headers forwarded: %v", resp.Header)
	}
	if resp.Header.Get("X-Ok") != "fine" {
		t.Fatalf("valid header dropped: %v", resp.Header)
	}
}

func TestWorkerPanicTruncatesBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
		panic("boom")
	})
	srv := &Server{HTTPHandler: mux, Workers: 1, HeaderTimeout: 2 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /boom HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || len(body) != 0 {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

func TestLargeResponseAboveMaxOutputServed(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 2048)
	rawMux := &Mux{}
	rawMux.HandleFunc("/big", func(req *Request, res *Response) { res.Body = body })
	adapterMux := http.NewServeMux()
	adapterMux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	})
	servers := map[string]*Server{
		"raw":     {Handler: rawMux, MaxOutputBytes: 128, HeaderTimeout: 2 * time.Second},
		"adapter": {HTTPHandler: adapterMux, MaxOutputBytes: 128, HeaderTimeout: 2 * time.Second},
	}
	for name, srv := range servers {
		t.Run(name, func(t *testing.T) {
			ln, cleanup := startTestServer(t, srv)
			defer cleanup()
			code, got, err := dialRead(t, ln.Addr().String(), "GET /big HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
			if err != nil || code != 200 {
				t.Fatalf("code=%d err=%v", code, err)
			}
			if got != string(body) {
				t.Fatalf("body length = %d, want %d", len(got), len(body))
			}
		})
	}
}

func TestHeaderBombClosedPromptly(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/", func(req *Request, res *Response) { res.Body = []byte("ok") })
	srv := &Server{Handler: mux, HeaderTimeout: 30 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nX-Big: "+strings.Repeat("a", 9000)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var b [1]byte
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("oversized header connection stayed open")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("oversized header closed after %v", elapsed)
	}
}

func TestParseFuzzSeeds(t *testing.T) {
	seeds := []string{
		"GET / HTTP/1.1\r\nHost: x\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 3\r\n\r\nabc",
		"GET / HTTP/1.0\r\nConnection: keep-alive\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nContent-Length: 4\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 9000\r\n\r\n",
	}
	for _, seed := range seeds {
		func() {
			f, c := newParseHarness(t, &Mux{})
			buf := f.rx.Bytes(c.rxBid)
			copy(buf, seed)
			c.rxEnd = int32(len(seed))
			_, _, _ = f.parse(c)
		}()
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"GET / HTTP/1.1\r\nHost: x\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 3\r\n\r\nabc",
		"GET / HTTP/1.0\r\nConnection: keep-alive\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nContent-Length: 4\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 9000\r\n\r\n",
		"HEAD / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fastInSize {
			return
		}
		fl, c := newParseHarness(t, &Mux{})
		copy(fl.rx.Bytes(c.rxBid), data)
		c.rxEnd = int32(len(data))
		_, _, _ = fl.parse(c)
	})
}

func TestRawHandlerPanicReturns500(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/boom", func(req *Request, res *Response) { panic("boom") })
	mux.HandleFunc("/ok", func(req *Request, res *Response) { res.Body = []byte("ok") })
	srv := &Server{Handler: mux, HeaderTimeout: 2 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	code, body, err := dialRead(t, ln.Addr().String(), "GET /boom HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if err != nil || code != 500 || body != "" {
		t.Fatalf("panic: code=%d body=%q err=%v", code, body, err)
	}
	code, body, err = dialRead(t, ln.Addr().String(), "GET /ok HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if err != nil || code != 200 || body != "ok" {
		t.Fatalf("follow-up: code=%d body=%q err=%v", code, body, err)
	}
}

func TestConcurrentServeDefaultsOnce(t *testing.T) {
	mux := &Mux{}
	mux.HandleFunc("/ok", func(req *Request, res *Response) { res.Body = []byte("ok") })
	srv := &Server{Handler: mux}
	ln1, err := netioListenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	ln2, err := netioListenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 2)
	go func() { errCh <- srv.Serve(ln1) }()
	go func() { errCh <- srv.Serve(ln2) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.loops)
		srv.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d loops started", n)
		}
		time.Sleep(time.Millisecond)
	}
	if srv.HeaderTimeout != 10*time.Second || srv.IdleTimeout != 2*time.Minute ||
		srv.WriteTimeout != 30*time.Second || srv.MaxOutputBytes != 256<<10 {
		t.Fatalf("defaults = %v %v %v %d", srv.HeaderTimeout, srv.IdleTimeout, srv.WriteTimeout, srv.MaxOutputBytes)
	}
	code, body, err := dialRead(t, ln1.Addr().String(), "GET /ok HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if err != nil || code != 200 || body != "ok" {
		t.Fatalf("request: code=%d body=%q err=%v", code, body, err)
	}
	_ = srv.Close()
	for i := 0; i < 2; i++ {
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not return after Close")
		}
	}
}

func TestConcurrentKeepAliveIsolation(t *testing.T) {
	pad := bytes.Repeat([]byte("p"), 1024)
	mux := &Mux{}
	mux.HandleFunc("/echo", func(req *Request, res *Response) { res.Body = req.Body })
	mux.HandleFunc("/pad", func(req *Request, res *Response) { res.Body = pad })
	srv := &Server{
		Handler:       mux,
		HeaderTimeout: time.Second,
		IdleTimeout:   50 * time.Millisecond,
		WriteTimeout:  time.Second,
	}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()

	const clients = 32
	var wg sync.WaitGroup
	errCh := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for round := 0; round < 10; round++ {
				body := fmt.Sprintf("client-%02d-round-%02d", i, round)
				raw := fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
					len(body), body)
				code, got, err := dialRead(t, ln.Addr().String(), raw)
				if err != nil || code != 200 || got != body {
					errCh <- fmt.Errorf("client %d round %d: code=%d body=%q err=%v", i, round, code, got, err)
					return
				}
				code, got, err = dialRead(t, ln.Addr().String(), "GET /pad HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
				if err != nil || code != 200 || got != string(pad) {
					errCh <- fmt.Errorf("client %d round %d pad: code=%d len=%d err=%v", i, round, code, len(got), err)
					return
				}
			}
		}(i)
	}
	// Abrupt half-open requests exercise close with a receive outstanding.
	for i := 0; i < 100; i++ {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, "GET /pad HTTP/1.1\r\nHost: x\r\n")
		_ = conn.Close()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestHTTP10DefaultClose(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	srv := &Server{HTTPHandler: mux, HeaderTimeout: 2 * time.Second}
	ln, cleanup := startTestServer(t, srv)
	defer cleanup()
	addr := ln.Addr().String()

	// HTTP/1.0 closes by default.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(conn, "GET /ok HTTP/1.0\r\nHost: x\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok" || !resp.Close {
		t.Fatalf("default: status=%d body=%q close=%v", resp.StatusCode, body, resp.Close)
	}
	var one [1]byte
	if _, err := br.Read(one[:]); err == nil {
		t.Fatal("HTTP/1.0 connection stayed open without keep-alive")
	}
	_ = conn.Close()

	// An explicit keep-alive keeps the connection usable.
	conn, err = net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /ok HTTP/1.0\r\nHost: x\r\nConnection: keep-alive\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br = bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		resp, err = http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("keep-alive response %d: %v", i, err)
		}
		body, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.Close || string(body) != "ok" {
			t.Fatalf("keep-alive response %d: close=%v body=%q", i, resp.Close, body)
		}
		if i == 0 {
			_, _ = io.WriteString(conn, "GET /ok HTTP/1.0\r\nHost: x\r\nConnection: keep-alive\r\n\r\n")
		}
	}
}

func TestResponseContentLengthRules(t *testing.T) {
	date := []byte("Wed, 11 Nov 2026 00:00:00 GMT")
	raw := func(res *Response, head bool) []byte {
		return appendResponse(nil, res, true, head, "srv", date)
	}
	adapted := func(method string, status int, header http.Header) []byte {
		job := &httpJob{req: http.Request{Method: method}, respHeader: header}
		return appendHTTPResponse(nil, job, status, false, "srv", date)
	}
	read := func(t *testing.T, out []byte, method string) (http.Header, string) {
		t.Helper()
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(out)), &http.Request{Method: method})
		if err != nil {
			t.Fatalf("read response %q: %v", out, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.Header, string(body)
	}
	withLength := func(n string) http.Header {
		h := make(http.Header)
		h.Set("Content-Length", n)
		return h
	}

	cases := []struct {
		name   string
		out    []byte
		method string
		length string
		body   string
	}{
		{"raw-head-empty", raw(&Response{Status: 200}, true), "HEAD", "", ""},
		{"raw-head-body", raw(&Response{Status: 200, Body: []byte("abc")}, true), "HEAD", "3", ""},
		{"raw-304", raw(&Response{Status: 304, Body: []byte("abc")}, false), "GET", "", ""},
		{"raw-204", raw(&Response{Status: 204, Body: []byte("abc")}, false), "GET", "", ""},
		{"raw-get-empty", raw(&Response{Status: 200}, false), "GET", "0", ""},
		{"adapter-head-empty", adapted("HEAD", 200, nil), "HEAD", "", ""},
		{"adapter-head-handler-length", adapted("HEAD", 200, withLength("7")), "HEAD", "7", ""},
		{"adapter-304", adapted("GET", 304, nil), "GET", "", ""},
		{"adapter-304-handler-length", adapted("GET", 304, withLength("9")), "GET", "9", ""},
		{"adapter-204", adapted("GET", 204, withLength("5")), "GET", "", ""},
		{"adapter-get-empty", adapted("GET", 200, nil), "GET", "0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header, body := read(t, tc.out, tc.method)
			if got := header.Get("Content-Length"); got != tc.length {
				t.Fatalf("Content-Length = %q, want %q", got, tc.length)
			}
			if body != tc.body {
				t.Fatalf("body = %q, want %q", body, tc.body)
			}
		})
	}
}

func TestPanicClearsResponseHeaders(t *testing.T) {
	modes := map[string]func(*http.ServeMux) *Server{
		"inline": func(mux *http.ServeMux) *Server {
			return &Server{HTTPHandler: mux, HeaderTimeout: 2 * time.Second}
		},
		"worker": func(mux *http.ServeMux) *Server {
			return &Server{HTTPHandler: mux, Workers: 1, HeaderTimeout: 2 * time.Second}
		},
	}
	for name, newServer := range modes {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Secret", "leak")
				_, _ = w.Write([]byte("secret"))
				panic("boom")
			})
			srv := newServer(mux)
			ln, cleanup := startTestServer(t, srv)
			defer cleanup()

			conn, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_, _ = io.WriteString(conn, "GET /boom HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 500 || len(body) != 0 {
				t.Fatalf("status=%d body=%q", resp.StatusCode, body)
			}
			if got := resp.Header.Get("X-Secret"); got != "" {
				t.Fatalf("panic leaked header %q", got)
			}
		})
	}
}
