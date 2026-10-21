package event_bench

import (
	"testing"

	"github.com/encryptedbusiness/encryl/event"
)

type benchConn struct{}

func noopHandler(*benchConn, event.Message) error { return nil }

func BenchmarkRegistryDispatch(b *testing.B) {
	r := event.NewRegistry[*benchConn]()
	if err := r.On(1024, noopHandler); err != nil {
		b.Fatal(err)
	}
	c := &benchConn{}
	wire := event.EncodeMessage(event.Envelope{Kind: event.KindEvent, Event: 1024}, []byte("payload"))
	e, err := event.Decode(wire)
	if err != nil {
		b.Fatal(err)
	}
	h, _ := r.Lookup(e.Event)
	m := event.Message{Event: e.Event, RequestID: e.RequestID, Payload: e.Payload, Binary: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h(c, m)
	}
}

func BenchmarkPendingBindTake(b *testing.B) {
	p := event.NewPending(16)
	cb := func(event.Ack) {}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		token, _, _ := p.Bind(0, 1, 1, 1024, cb)
		_, _ = p.Take(token, 0, 1, 1)
	}
}
