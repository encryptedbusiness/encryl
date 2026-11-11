//go:build linux

package httpuring

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/encryptedbusiness/encryl/netio"
	"golang.org/x/sys/unix"
)

const idleConns = 2000

func TestFastConnSize(t *testing.T) {
	if n := unsafe.Sizeof(fastConn{}); n > 192 {
		t.Fatalf("slot is %d bytes", n)
	}
}

func TestIdleConnectionMemory(t *testing.T) {
	if os.Getenv("HTTPURING_IDLE_CHILD") == "1" {
		runIdleChild(t)
		return
	}
	raiseNoFile(t)
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestIdleConnectionMemory$", "-test.v=false")
	cmd.Env = append(os.Environ(), "HTTPURING_IDLE_CHILD=1", "HTTPURING_IDLE_DIR="+dir)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	addr := waitFile(t, filepath.Join(dir, "addr"), 20*time.Second)
	conns := make([]net.Conn, 0, idleConns)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < idleConns; i++ {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		if _, err := io.WriteString(c, "GET /healthz HTTP/1.1\r\nHost: x\r\nConnection: keep-alive\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("response %d: %v", i, err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		conns = append(conns, c)
	}

	text := waitFile(t, filepath.Join(dir, "result"), 20*time.Second)
	fields := strings.Fields(text)
	if len(fields) != 2 {
		t.Fatalf("result %q", text)
	}
	per, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatal(err)
	}
	inUse, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	if inUse != 0 {
		t.Fatalf("idle arenas still hold %d buffers", inUse)
	}
	if per > 410 {
		t.Fatalf("idle heap %d bytes per connection", per)
	}
	t.Logf("idle heap: %d bytes/connection over %d connections; %d arena buffers in use", per, idleConns, inUse)
}

func waitFile(t *testing.T, path string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(b)) != "" {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}

func raiseNoFile(t *testing.T) {
	t.Helper()
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		t.Fatal(err)
	}
	lim.Cur = lim.Max
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		t.Fatal(err)
	}
}

func runIdleChild(t *testing.T) {
	raiseNoFile(t)
	dir := os.Getenv("HTTPURING_IDLE_DIR")
	mux := &Mux{}
	mux.HandleFunc("/healthz", func(req *Request, res *Response) {
		res.Body = []byte("ok")
	})
	ln, err := netio.Listen("127.0.0.1:0", false, 65535)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Handler:       mux,
		HeaderTimeout: time.Minute,
		IdleTimeout:   time.Hour,
		WriteTimeout:  time.Minute,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		srv.mu.Lock()
		ready := len(srv.loops) == 1
		srv.mu.Unlock()
		if ready {
			break
		}
		select {
		case err := <-errCh:
			t.Fatal(err)
		default:
			time.Sleep(time.Millisecond)
		}
	}

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	if err := os.WriteFile(filepath.Join(dir, "addr"), []byte(ln.Addr().String()), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(20 * time.Second)
	var live, inUse int
	for time.Now().Before(deadline) {
		srv.mu.Lock()
		if len(srv.loops) == 1 {
			f := srv.loops[0]
			live = f.slots.live
			inUse = f.rx.InUse() + f.tx.InUse()
		}
		srv.mu.Unlock()
		if live == idleConns && inUse == 0 {
			break
		}
		select {
		case err := <-errCh:
			t.Fatal(err)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if live != idleConns || inUse != 0 {
		t.Fatalf("not idle live=%d inUse=%d", live, inUse)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	per := 0
	if after.HeapAlloc > base.HeapAlloc {
		per = int((after.HeapAlloc - base.HeapAlloc) / uint64(idleConns))
	}
	if err := os.WriteFile(filepath.Join(dir, "result"), []byte(fmt.Sprintf("%d %d\n", per, inUse)), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	_ = srv.Close()
	<-errCh
}
