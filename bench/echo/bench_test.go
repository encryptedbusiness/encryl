//go:build linux

package echo_bench

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	encryl "github.com/encryptedbusiness/encryl"
	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/protocol"
)

func testServerConfig() encryl.Config {
	cfg := encryl.DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.Loops = 1
	cfg.MaxConnections = 128
	cfg.ReadBufferSize = 4096
	cfg.BufferCount = 64
	cfg.TxBytes = 2 << 20
	cfg.AllowedOrigins = []string{"*"}
	cfg.HandshakeTimeout = 3 * time.Second
	cfg.IdleTimeout = time.Minute
	cfg.PingInterval = 10 * time.Second
	cfg.ShutdownTimeout = 3 * time.Second
	return cfg
}

func startServer(tb testing.TB, srv *encryl.Server) string {
	tb.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for srv.Addr() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	addr := srv.Addr()
	if addr == nil {
		cancel()
		tb.Fatal("server did not start")
	}
	tb.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				tb.Errorf("ListenAndServe: %v", err)
			}
		case <-time.After(3 * time.Second):
			tb.Error("ListenAndServe did not return")
		}
	})
	return "ws://" + addr.String() + "/ws"
}

func maskedBinaryFrame(payload []byte, mask [4]byte) []byte {
	out := make([]byte, 2, 2+4+len(payload))
	out[0] = 0x82
	switch {
	case len(payload) < 126:
		out[1] = 0x80 | byte(len(payload))
	case len(payload) <= 65535:
		out[1] = 0x80 | 126
		out = binary.BigEndian.AppendUint16(out, uint16(len(payload)))
	default:
		out[1] = 0x80 | 127
		out = binary.BigEndian.AppendUint64(out, uint64(len(payload)))
	}
	out = append(out, mask[:]...)
	masked := append([]byte(nil), payload...)
	for i := range masked {
		masked[i] ^= mask[i&3]
	}
	return append(out, masked...)
}

func rawWSDial(tb testing.TB, rawURL string) net.Conn {
	return rawWSDialProtocol(tb, rawURL, "encryl.v2")
}

func rawWSDialProtocol(tb testing.TB, rawURL, subprotocol string) net.Conn {
	tb.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		tb.Fatal(err)
	}
	c, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err != nil {
		tb.Fatal(err)
	}
	req := "GET " + u.Path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Protocol: " + subprotocol + "\r\n" +
		"\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		c.Close()
		tb.Fatal(err)
	}
	var resp []byte
	one := make([]byte, 1)
	for !bytes.HasSuffix(resp, []byte("\r\n\r\n")) {
		if len(resp) > 4096 {
			c.Close()
			tb.Fatal("handshake response too large")
		}
		if _, err := io.ReadFull(c, one); err != nil {
			c.Close()
			tb.Fatal(err)
		}
		resp = append(resp, one[0])
	}
	if !bytes.HasPrefix(resp, []byte("HTTP/1.1 101")) {
		c.Close()
		tb.Fatalf("handshake failed: %q", resp)
	}
	return c
}

func readWSMessage(b *testing.B, c net.Conn, buf []byte) []byte {
	b.Helper()
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		b.Fatal(err)
	}
	n := int(buf[1] & 0x7f)
	off := 2
	switch n {
	case 126:
		if _, err := io.ReadFull(c, buf[2:4]); err != nil {
			b.Fatal(err)
		}
		n = int(binary.BigEndian.Uint16(buf[2:4]))
		off = 4
	case 127:
		if _, err := io.ReadFull(c, buf[2:10]); err != nil {
			b.Fatal(err)
		}
		n = int(binary.BigEndian.Uint64(buf[2:10]))
		off = 10
	}
	if n > len(buf)-off {
		b.Fatalf("frame too large: %d", n)
	}
	if _, err := io.ReadFull(c, buf[off:off+n]); err != nil {
		b.Fatal(err)
	}
	return buf[off : off+n]
}

// BenchmarkServerEcho100k measures the full public path: envelope decode,
// handler dispatch, Conn.Emit, write queue, WS framing, and send.
func BenchmarkServerEcho100k(b *testing.B) {
	srv, err := encryl.New(testServerConfig())
	if err != nil {
		b.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *encryl.Conn, m encryl.Message) error {
		return c.EmitID(1024, m.Payload)
	}); err != nil {
		b.Fatal(err)
	}
	rawURL := startServer(b, srv)
	c := rawWSDial(b, rawURL)
	defer c.Close()

	payload := event.EncodeMessage(event.Envelope{Kind: event.KindEvent, Event: 1024}, []byte("0123456789abcdef"))
	frame := maskedBinaryFrame(payload, [4]byte{0x37, 0xfa, 0x21, 0x3d})
	hdr := make([]byte, 2)
	body := make([]byte, 64)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload) * 2))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Write(frame); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(c, hdr); err != nil {
			b.Fatal(err)
		}
		n := int(hdr[1] & 0x7f)
		if n >= 126 {
			b.Fatalf("unexpected extended length %d", n)
		}
		if _, err := io.ReadFull(c, body[:n]); err != nil {
			b.Fatal(err)
		}
		if n < event.HeaderSize || body[0] != event.Version {
			b.Fatalf("bad echo envelope: n=%d", n)
		}
		if binary.BigEndian.Uint32(body[4:8]) != 1024 {
			b.Fatalf("event = %d", binary.BigEndian.Uint32(body[4:8]))
		}
	}
}

// BenchmarkStringEcho100k is BenchmarkServerEcho100k with the named-event API.
func BenchmarkStringEcho100k(b *testing.B) {
	srv, err := encryl.New(testServerConfig())
	if err != nil {
		b.Fatal(err)
	}
	if err := srv.On("echo", func(c *encryl.Conn, data []byte) {
		_ = c.Emit("echo", data)
	}); err != nil {
		b.Fatal(err)
	}
	rawURL := startServer(b, srv)
	c := rawWSDial(b, rawURL)
	defer c.Close()

	id := encryl.EventIDForName("echo")
	payload := event.EncodeMessage(event.Envelope{Kind: event.KindEvent, Event: id}, []byte("0123456789abcdef"))
	frame := maskedBinaryFrame(payload, [4]byte{0x37, 0xfa, 0x21, 0x3d})
	hdr := make([]byte, 2)
	body := make([]byte, 64)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload) * 2))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Write(frame); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(c, hdr); err != nil {
			b.Fatal(err)
		}
		n := int(hdr[1] & 0x7f)
		if n >= 126 {
			b.Fatalf("unexpected extended length %d", n)
		}
		if _, err := io.ReadFull(c, body[:n]); err != nil {
			b.Fatal(err)
		}
		if n < event.HeaderSize || body[0] != event.Version {
			b.Fatalf("bad echo envelope: n=%d", n)
		}
		if binary.BigEndian.Uint32(body[4:8]) != id {
			b.Fatalf("event = %d", binary.BigEndian.Uint32(body[4:8]))
		}
	}
}

// BenchmarkTopicEcho100k is the named-event path with a precomputed topic.
func BenchmarkTopicEcho100k(b *testing.B) {
	srv, err := encryl.New(testServerConfig())
	if err != nil {
		b.Fatal(err)
	}
	echo := srv.Topic("echo")
	if err := echo.On(func(c *encryl.Conn, data []byte) {
		_ = echo.Emit(c, data)
	}); err != nil {
		b.Fatal(err)
	}
	rawURL := startServer(b, srv)
	c := rawWSDial(b, rawURL)
	defer c.Close()

	payload := event.EncodeMessage(event.Envelope{Kind: event.KindEvent, Event: echo.ID()}, []byte("0123456789abcdef"))
	frame := maskedBinaryFrame(payload, [4]byte{0x37, 0xfa, 0x21, 0x3d})
	hdr := make([]byte, 2)
	body := make([]byte, 64)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload) * 2))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Write(frame); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(c, hdr); err != nil {
			b.Fatal(err)
		}
		n := int(hdr[1] & 0x7f)
		if n >= 126 {
			b.Fatalf("unexpected extended length %d", n)
		}
		if _, err := io.ReadFull(c, body[:n]); err != nil {
			b.Fatal(err)
		}
		if n < event.HeaderSize || body[0] != event.Version {
			b.Fatalf("bad echo envelope: n=%d", n)
		}
	}
}

// BenchmarkBebopSendEcho100k measures the Bebop send path.
func BenchmarkBebopSendEcho100k(b *testing.B) {
	cfg := testServerConfig()
	cfg.EventNames = map[string]uint32{"echo": 2048}
	srv, err := encryl.New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	if err := srv.OnID(2048, func(c *encryl.Conn, m encryl.Message) error {
		return c.EmitID(2048, m.Payload)
	}); err != nil {
		b.Fatal(err)
	}
	rawURL := startServer(b, srv)
	c := rawWSDialProtocol(b, rawURL, protocol.SubprotocolBebopV1)
	defer c.Close()

	connect := protocol.Command{ID: 1, Kind: protocol.CommandConnect, Connect: protocol.Connect{}}
	if _, err := c.Write(maskedBinaryFrame(protocol.AppendFramedCommandBebop(nil, &connect), [4]byte{1, 2, 3, 4})); err != nil {
		b.Fatal(err)
	}
	buf := make([]byte, 2048)
	readWSMessage(b, c, buf)

	send := protocol.Command{
		Kind: protocol.CommandSend,
		Send: protocol.Send{Event: protocol.EventRef{ID: 2048}, Payload: []byte("0123456789abcdef")},
	}
	frame := maskedBinaryFrame(protocol.AppendFramedCommandBebop(nil, &send), [4]byte{0x37, 0xfa, 0x21, 0x3d})
	var replies []protocol.Reply
	b.ReportAllocs()
	b.SetBytes(int64(len(frame) * 2))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Write(frame); err != nil {
			b.Fatal(err)
		}
		data := readWSMessage(b, c, buf)
		var derr error
		replies, derr = protocol.DecodeBebopReplies(data, replies[:0])
		if derr != nil {
			b.Fatal(derr)
		}
		if len(replies) != 1 || replies[0].Push.Kind != protocol.PushMessage ||
			replies[0].Push.Message.EventID != 2048 {
			b.Fatalf("unexpected push: %+v", replies)
		}
	}
}
