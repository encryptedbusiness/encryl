package ws_bench

import (
	"testing"

	"github.com/encryptedbusiness/encryl/ws"
)

func BenchmarkParseSingleFrame(b *testing.B) {
	p := ws.NewParser(ws.DefaultLimits())
	payload := []byte("0123456789abcdef")
	mask := [4]byte{1, 2, 3, 4}
	wire := make([]byte, 2+4+len(payload))
	wire[0] = 0x82
	wire[1] = 0x80 | byte(len(payload))
	copy(wire[2:6], mask[:])
	copy(wire[6:], payload)
	ws.Mask(wire[6:], mask, 0)
	buf := make([]byte, len(wire))
	handler := func(ws.Event) error { return nil }
	b.ReportAllocs()
	b.SetBytes(int64(len(wire)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf, wire)
		p.Reset()
		if err := p.Feed(buf, handler); err != nil {
			b.Fatal(err)
		}
	}
}
