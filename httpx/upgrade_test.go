package httpx

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const validRequest = "GET /ws HTTP/1.1\r\n" +
	"Host: example.com\r\n" +
	"Upgrade: websocket\r\n" +
	"Connection: keep-alive, Upgrade\r\n" +
	"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
	"Sec-WebSocket-Version: 13\r\n" +
	"Sec-WebSocket-Protocol: foo, encryl.v2\r\n" +
	"Origin: https://example.com\r\n" +
	"\r\n"

const rfcAccept = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="

func TestValidRequest(t *testing.T) {
	p := NewParser(DefaultLimits())
	res, consumed, err := p.Feed([]byte(validRequest))
	if err != nil {
		t.Fatal(err)
	}
	if res != Complete {
		t.Fatalf("result = %v, want Complete", res)
	}
	if consumed != len(validRequest) {
		t.Fatalf("consumed = %d, want %d", consumed, len(validRequest))
	}
	req := p.Request()
	if string(req.Accept[:]) != rfcAccept {
		t.Fatalf("accept = %q, want %q", req.Accept[:], rfcAccept)
	}
	if string(req.Host) != "example.com" {
		t.Fatalf("host = %q", req.Host)
	}
	if !req.HasOrigin || string(req.Origin) != "https://example.com" {
		t.Fatalf("origin = %q, has=%v", req.Origin, req.HasOrigin)
	}
}

func TestSplitEveryByte(t *testing.T) {
	for step := 1; step <= len(validRequest); step++ {
		p := NewParser(DefaultLimits())
		data := []byte(validRequest)
		var res Result
		var consumed int
		for len(data) > 0 {
			n := step
			if n > len(data) {
				n = len(data)
			}
			var err error
			res, consumed, err = p.Feed(data[:n])
			if err != nil {
				t.Fatalf("step %d: %v", step, err)
			}
			data = data[n:]
			if res == Complete {
				break
			}
		}
		if res != Complete {
			t.Fatalf("step %d: result = %v", step, res)
		}
		_ = consumed
		req := p.Request()
		if string(req.Accept[:]) != rfcAccept {
			t.Fatalf("step %d: accept mismatch", step)
		}
	}
}

func TestTrailingBytesAreNotConsumed(t *testing.T) {
	p := NewParser(DefaultLimits())
	trailing := []byte{0x82, 0x80, 1, 2, 3, 4}
	data := append([]byte(validRequest), trailing...)
	res, consumed, err := p.Feed(data)
	if err != nil || res != Complete {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if consumed != len(validRequest) {
		t.Fatalf("consumed = %d, want %d", consumed, len(validRequest))
	}
	if !bytes.Equal(data[consumed:], trailing) {
		t.Fatalf("trailing = %v", data[consumed:])
	}
}

func TestRejects(t *testing.T) {
	mutate := func(f func(string) string) string { return f(validRequest) }
	tests := []struct {
		name   string
		req    string
		status int
	}{
		{"bad method", strings.Replace(validRequest, "GET ", "POST ", 1), 405},
		{"http 1.0", strings.Replace(validRequest, "HTTP/1.1", "HTTP/1.0", 1), 400},
		{"relative path", strings.Replace(validRequest, "GET /ws", "GET ws", 1), 400},
		{"missing host", mutate(func(s string) string { return strings.Replace(s, "Host: example.com\r\n", "", 1) }), 400},
		{"missing upgrade", mutate(func(s string) string { return strings.Replace(s, "Upgrade: websocket\r\n", "", 1) }), 400},
		{"missing connection", mutate(func(s string) string { return strings.Replace(s, "Connection: keep-alive, Upgrade\r\n", "", 1) }), 400},
		{"missing version", mutate(func(s string) string { return strings.Replace(s, "Sec-WebSocket-Version: 13\r\n", "", 1) }), 400},
		{"wrong version", strings.Replace(validRequest, "Version: 13", "Version: 12", 1), 426},
		{"missing key", mutate(func(s string) string {
			return strings.Replace(s, "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n", "", 1)
		}), 400},
		{"bad key", strings.Replace(validRequest, "dGhlIHNhbXBsZSBub25jZQ==", "not-base64", 1), 400},
		{"wrong protocol", strings.Replace(validRequest, "foo, encryl.v2", "foo", 1), 400},
		{"body", strings.Replace(validRequest, "Origin:", "Content-Length: 5\r\nOrigin:", 1), 400},
		{"transfer encoding", strings.Replace(validRequest, "Origin:", "Transfer-Encoding: chunked\r\nOrigin:", 1), 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser(DefaultLimits())
			res, _, err := p.Feed([]byte(tc.req))
			if res != Rejected {
				t.Fatalf("result = %v, want Rejected", res)
			}
			var rej *RejectError
			if !errors.As(err, &rej) || rej.Status != tc.status {
				t.Fatalf("err = %v, want status %d", err, tc.status)
			}
		})
	}
}

func TestDuplicateKeyRejected(t *testing.T) {
	req := strings.Replace(validRequest, "Sec-WebSocket-Version:",
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version:", 1)
	p := NewParser(DefaultLimits())
	res, _, err := p.Feed([]byte(req))
	var rej *RejectError
	if res != Rejected || !errors.As(err, &rej) || rej.Status != 400 {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

func TestHeaderLimits(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		p := NewParser(Limits{MaxHeaderBytes: 8192, MaxHeaders: 2})
		res, _, err := p.Feed([]byte(validRequest))
		var rej *RejectError
		if res != Rejected || !errors.As(err, &rej) || rej.Status != 431 {
			t.Fatalf("res=%v err=%v", res, err)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		p := NewParser(Limits{MaxHeaderBytes: 40, MaxHeaders: 100})
		res, _, err := p.Feed([]byte(validRequest))
		var rej *RejectError
		if res != Rejected || !errors.As(err, &rej) || rej.Status != 431 {
			t.Fatalf("res=%v err=%v", res, err)
		}
	})
}

func TestBareLineEndings(t *testing.T) {
	for _, req := range []string{
		strings.ReplaceAll(validRequest, "\r\n", "\n"),
		strings.ReplaceAll(validRequest, "\r\n", "\r"),
	} {
		p := NewParser(DefaultLimits())
		res, _, err := p.Feed([]byte(req))
		var rej *RejectError
		if res != Rejected || !errors.As(err, &rej) || rej.Status != 400 {
			t.Fatalf("res=%v err=%v", res, err)
		}
	}
}

func TestResponse101(t *testing.T) {
	p := NewParser(DefaultLimits())
	if _, _, err := p.Feed([]byte(validRequest)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	req := p.Request()
	n := Response101(buf, req.Accept, req.Subprotocol)
	resp := string(buf[:n])
	if !strings.HasPrefix(resp, "HTTP/1.1 101 Switching Protocols\r\n") {
		t.Fatalf("response = %q", resp)
	}
	if !strings.Contains(resp, "Sec-WebSocket-Accept: "+rfcAccept+"\r\n") {
		t.Fatalf("response = %q", resp)
	}
	if !strings.Contains(resp, "Sec-WebSocket-Protocol: encryl.v2\r\n") {
		t.Fatalf("response = %q", resp)
	}
	if !strings.HasSuffix(resp, "\r\n\r\n") {
		t.Fatalf("response does not end with a blank line: %q", resp)
	}
}

func TestRejectResponse(t *testing.T) {
	resp := string(RejectResponse(400, "bad request"))
	if !strings.HasPrefix(resp, "HTTP/1.1 400 bad request\r\n") {
		t.Fatalf("response = %q", resp)
	}
	if !strings.Contains(resp, "Connection: close\r\n") {
		t.Fatalf("response = %q", resp)
	}
}

func TestResetRetainsSmallBuffers(t *testing.T) {
	p := NewParser(DefaultLimits())
	if _, _, err := p.Feed([]byte(validRequest)); err != nil {
		t.Fatal(err)
	}
	p.Reset()
	if len(p.line) != 0 || len(p.host) != 0 || len(p.origin) != 0 {
		t.Fatal("Reset did not truncate parser buffers")
	}
	if cap(p.line) == 0 {
		t.Fatal("Reset released a small line buffer")
	}
	// A buffer that grew past the retention limit must be released so one
	// oversized request cannot pin memory in the pool.
	p.line = make([]byte, 0, 9000)
	p.Reset()
	if p.line != nil {
		t.Fatal("Reset retained an oversized line buffer")
	}
}

func FuzzUpgrade(f *testing.F) {
	f.Add([]byte(validRequest))
	f.Add([]byte("GET / HTTP/1.1\r\n\r\n"))
	f.Add([]byte("GET / HTTP/1.1\n\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		p := NewParser(DefaultLimits())
		res, consumed, err := p.Feed(data)
		if consumed < 0 || consumed > len(data) {
			t.Fatalf("consumed = %d for %d bytes", consumed, len(data))
		}
		if res == Complete && err != nil {
			t.Fatal("Complete with an error")
		}
		if res == Rejected && err == nil {
			t.Fatal("Rejected without an error")
		}
	})
}

func TestProtobufSubprotocolNegotiation(t *testing.T) {
	req := strings.Replace(validRequest, "foo, encryl.v2", "encryl.pb.v1", 1)
	p := NewParser(DefaultLimits())
	res, _, err := p.Feed([]byte(req))
	if err != nil || res != Complete {
		t.Fatalf("res=%v err=%v", res, err)
	}
	got := p.Request()
	if got.Encoding != 1 || string(got.Subprotocol) != "encryl.pb.v1" {
		t.Fatalf("request = encoding %d subprotocol %q", got.Encoding, got.Subprotocol)
	}
	buf := make([]byte, 256)
	n := Response101(buf, got.Accept, got.Subprotocol)
	if !strings.Contains(string(buf[:n]), "Sec-WebSocket-Protocol: encryl.pb.v1\r\n") {
		t.Fatalf("response = %q", string(buf[:n]))
	}
}

func TestProtobufFormatQuery(t *testing.T) {
	req := strings.Replace(validRequest, "GET /ws HTTP/1.1", "GET /ws?format=protobuf HTTP/1.1", 1)
	req = strings.Replace(req, "Sec-WebSocket-Protocol: foo, encryl.v2\r\n", "", 1)
	p := NewParser(DefaultLimits())
	res, _, err := p.Feed([]byte(req))
	if err != nil || res != Complete {
		t.Fatalf("res=%v err=%v", res, err)
	}
	got := p.Request()
	if got.Encoding != 1 || len(got.Subprotocol) != 0 {
		t.Fatalf("request = encoding %d subprotocol %q", got.Encoding, got.Subprotocol)
	}
	buf := make([]byte, 256)
	n := Response101(buf, got.Accept, got.Subprotocol)
	if strings.Contains(string(buf[:n]), "Sec-WebSocket-Protocol") {
		t.Fatalf("unexpected protocol header: %q", string(buf[:n]))
	}
}

func TestProtobufFormatConflict(t *testing.T) {
	req := strings.Replace(validRequest, "GET /ws HTTP/1.1", "GET /ws?format=protobuf HTTP/1.1", 1)
	p := NewParser(DefaultLimits())
	res, _, err := p.Feed([]byte(req))
	var rej *RejectError
	if res != Rejected || !errors.As(err, &rej) || rej.Status != 400 {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

func TestJSONFormatRejected(t *testing.T) {
	req := strings.Replace(validRequest, "GET /ws HTTP/1.1", "GET /ws?format=json HTTP/1.1", 1)
	p := NewParser(DefaultLimits())
	res, _, err := p.Feed([]byte(req))
	var rej *RejectError
	if res != Rejected || !errors.As(err, &rej) || rej.Status != 400 {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

func TestNoProtocolDefaultsToProtobuf(t *testing.T) {
	req := strings.Replace(validRequest, "Sec-WebSocket-Protocol: foo, encryl.v2\r\n", "", 1)
	p := NewParser(DefaultLimits())
	res, _, err := p.Feed([]byte(req))
	if err != nil || res != Complete {
		t.Fatalf("res=%v err=%v", res, err)
	}
	got := p.Request()
	if got.Encoding != 1 || len(got.Subprotocol) != 0 {
		t.Fatalf("request = encoding %d subprotocol %q", got.Encoding, got.Subprotocol)
	}
	buf := make([]byte, 256)
	n := Response101(buf, got.Accept, got.Subprotocol)
	if strings.Contains(string(buf[:n]), "Sec-WebSocket-Protocol") {
		t.Fatalf("unexpected protocol header: %q", string(buf[:n]))
	}
}
