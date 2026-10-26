package event

import (
	"testing"
)

type benchConn struct{}

func noopHandler(*benchConn, Message) error { return nil }

func TestRegistryDispatchDoesNotAllocate(t *testing.T) {
	r := NewRegistry[*benchConn]()
	if err := r.On(1024, noopHandler); err != nil {
		t.Fatal(err)
	}
	c := &benchConn{}
	wire := EncodeMessage(Envelope{Kind: KindEvent, Event: 1024}, []byte("payload"))
	e, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := r.Lookup(e.Event)
	m := Message{Event: e.Event, RequestID: e.RequestID, Payload: e.Payload, Binary: true}
	allocs := testing.AllocsPerRun(1000, func() {
		_ = h(c, m)
	})
	if allocs != 0 {
		t.Fatalf("lookup and dispatch allocated %.2f times per event", allocs)
	}
}

func TestPendingDoesNotAllocate(t *testing.T) {
	p := NewPending(16)
	cb := func(Ack) {}
	for i := 0; i < 16; i++ {
		token, _, ok := p.Bind(0, 1, 1, 1024, cb)
		if !ok {
			t.Fatal("warmup bind failed")
		}
		_, _ = p.Take(token, 0, 1, 1)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		token, _, ok := p.Bind(0, 1, 1, 1024, cb)
		if !ok {
			t.Fatal("bind failed")
		}
		_, _ = p.Take(token, 0, 1, 1)
	})
	if allocs != 0 {
		t.Fatalf("Bind/Take allocated %.2f times", allocs)
	}
}
