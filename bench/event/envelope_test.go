package event_bench

import (
	"testing"

	"github.com/encryptedbusiness/encryl/event"
)

func BenchmarkDecode(b *testing.B) {
	wire := event.EncodeMessage(event.Envelope{Kind: event.KindEvent, Event: 1024, RequestID: 7}, []byte("0123456789abcdef"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := event.Decode(wire); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeMessage(b *testing.B) {
	e := event.Envelope{Kind: event.KindEvent, Event: 1024, RequestID: 7}
	payload := []byte("0123456789abcdef")
	b.ReportAllocs()
	b.SetBytes(int64(event.HeaderSize + len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = event.EncodeMessage(e, payload)
	}
}
