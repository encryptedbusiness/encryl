//go:build linux

package httpuring

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServeHTTPOverURing(t *testing.T) {
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong\n"))
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	})

	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(ln)
	}()
	defer srv.Close()

	base := "http://" + ln.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "pong\n" {
		t.Fatalf("GET /ping = %d %q", resp.StatusCode, body)
	}

	req, err := http.NewRequest(http.MethodPost, base+"/echo", strings.NewReader("hello io_uring"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello io_uring" {
		t.Fatalf("POST /echo = %q", body)
	}

	// Exercise keep-alive and the background-read deadline path.
	resp, err = client.Get(base + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestListenerCloseUnblocksAccept(t *testing.T) {
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := ln.Accept()
		errCh <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Accept returned nil error after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not unblock after Close")
	}
}

var _ net.Listener = (*Listener)(nil)

var _ syscall.Conn = (*Conn)(nil)
