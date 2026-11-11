//go:build linux

package httpuring

import (
	"bufio"
	"io"
	"net"
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/netio"
)

func readBenchResponse(br *bufio.Reader, body []byte) error {
	for {
		line, err := br.ReadSlice('\n')
		if err != nil {
			return err
		}
		if len(line) == 2 && line[0] == '\r' {
			break
		}
	}
	_, err := io.ReadFull(br, body)
	return err
}

func BenchmarkKeepAliveLoopback(b *testing.B) {
	payload := []byte("Hello, World!")
	mux := &Mux{}
	mux.HandleFunc("/plaintext", func(req *Request, res *Response) { res.Body = payload })

	ln, err := netio.Listen("127.0.0.1:0", false, 4096)
	if err != nil {
		b.Fatal(err)
	}
	srv := &Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	addr := ln.Addr().String()
	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err = net.Dial("tcp", addr)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			b.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	defer conn.Close()

	req := []byte("GET /plaintext HTTP/1.1\r\nHost: x\r\n\r\n")
	br := bufio.NewReaderSize(conn, 4096)
	body := make([]byte, len(payload))
	for i := 0; i < 100; i++ {
		if _, err := conn.Write(req); err != nil {
			b.Fatal(err)
		}
		if err := readBenchResponse(br, body); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := conn.Write(req); err != nil {
			b.Fatal(err)
		}
		if err := readBenchResponse(br, body); err != nil {
			b.Fatal(err)
		}
	}
}
