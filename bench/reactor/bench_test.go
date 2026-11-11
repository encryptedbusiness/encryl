//go:build linux

package reactor_bench

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/netio"
	"github.com/encryptedbusiness/encryl/reactor"
)

func BenchmarkCommandRing(b *testing.B) {
	r := reactor.NewCommandRing(1024)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Push(reactor.Command{Kind: reactor.CmdSend})
		r.Pop()
	}
}

func BenchmarkSlabAcquireRelease(b *testing.B) {
	s := connslab.NewSlab(1024, 1024)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		slot, _, _ := s.Acquire(1)
		s.Transition(slot, connslab.StateClosed)
		s.Release(slot)
	}
}

func BenchmarkUserDataCodec(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		u := reactor.EncodeUserData(reactor.OpRecv, 1234, 7)
		_, _, _, _ = reactor.DecodeUserData(u)
	}
}

// BenchmarkRawEcho measures the reactor's receive->send path with no WebSocket
// framing and no envelope.
func BenchmarkRawEcho(b *testing.B) {
	ln, err := netio.Listen("127.0.0.1:0", false, 256)
	if err != nil {
		b.Fatal(err)
	}
	l, err := reactor.NewLoop(reactor.LoopConfig{
		Listener:        ln,
		ReadBufferSize:  4096,
		BufferCount:     64,
		TxBytes:         2 << 20,
		MaxConnections:  64,
		RingEntries:     256,
		RawEcho:         true,
		IdleTimeout:     time.Minute,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()

	c, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
	if err != nil {
		cancel()
		b.Fatal(err)
	}
	msg := []byte("0123456789abcdef")
	buf := make([]byte, len(msg))
	// Warm the accept path, queue, and first frame before timing.
	if _, err := c.Write(msg); err != nil {
		b.Fatal(err)
	}
	if _, err := io.ReadFull(c, buf); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(msg) * 2))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Write(msg); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(c, buf); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	_ = c.Close()
	cancel()
	_ = l.Shutdown()
	select {
	case <-done: //nolint:errcheck // wait for l.Run to return; the error is not asserted here
	case <-time.After(3 * time.Second):
		b.Fatal("loop did not stop")
	}
}
