package event

import "testing"

func TestPendingBindTake(t *testing.T) {
	p := NewPending(4)
	token, slot, ok := p.Bind(2, 9, 7, 1024, func(Ack) {})
	if !ok {
		t.Fatal("Bind failed")
	}
	if p.Len() != 1 {
		t.Fatalf("len = %d", p.Len())
	}
	entry, ok := p.Take(token, 2, 9, 7)
	if !ok || entry.Slot != slot || entry.Loop != 2 || entry.Event != 1024 || entry.Callback == nil {
		t.Fatalf("take = %+v %v", entry, ok)
	}
	if p.Len() != 0 {
		t.Fatalf("len after take = %d", p.Len())
	}
	if _, ok := p.Take(token, 2, 9, 7); ok {
		t.Fatal("double take succeeded")
	}
}

func TestPendingTimeout(t *testing.T) {
	p := NewPending(4)
	_, slot, _ := p.Bind(1, 3, 1, 1024, func(Ack) {})
	entry, ok := p.Timeout(slot, 1)
	if !ok || entry.Slot != slot {
		t.Fatalf("timeout = %+v %v", entry, ok)
	}
	if _, ok := p.Timeout(slot, 1); ok {
		t.Fatal("second timeout succeeded")
	}
}

func TestPendingStaleGeneration(t *testing.T) {
	p := NewPending(1)
	token1, slot, _ := p.Bind(0, 1, 1, 1, func(Ack) {})
	_, _ = p.Take(token1, 0, 1, 1)
	token2, slot2, ok := p.Bind(0, 1, 1, 2, func(Ack) {})
	if !ok || slot2 != slot {
		t.Fatalf("rebind = %d %d %v", token2, slot2, ok)
	}
	if token2 == token1 {
		t.Fatal("token did not advance generation")
	}
	if _, ok := p.Take(token1, 0, 1, 1); ok {
		t.Fatal("stale token accepted")
	}
	if _, ok := p.Take(token2, 0, 1, 1); !ok {
		t.Fatal("fresh token rejected")
	}
}

func TestPendingRejectsForeignConnection(t *testing.T) {
	p := NewPending(4)
	token, slot, _ := p.Bind(2, 9, 7, 1024, func(Ack) {})

	// Same loop, different slot: a peer on the same loop forged the token.
	if _, ok := p.Take(token, 2, 10, 7); ok {
		t.Fatal("take from a different slot succeeded")
	}
	// Same slot, different generation: the token predates a rebind.
	if _, ok := p.Take(token, 2, 9, 8); ok {
		t.Fatal("take with a different generation succeeded")
	}
	// Different loop.
	if _, ok := p.Take(token, 3, 9, 7); ok {
		t.Fatal("take from a different loop succeeded")
	}

	if p.Len() != 1 {
		t.Fatalf("foreign takes consumed the entry: len = %d", p.Len())
	}
	if _, ok := p.Take(token, 2, 9, 7); !ok {
		t.Fatal("owner could not take its own entry")
	}
	_ = slot
}

func TestPendingStaleTimerRejected(t *testing.T) {
	p := NewPending(1)
	_, slot, _ := p.Bind(0, 1, 1, 1, func(Ack) {})
	if _, ok := p.Timeout(slot, 2); ok {
		t.Fatal("timer with a future generation consumed the entry")
	}
	if p.Len() != 1 {
		t.Fatalf("stale timer consumed the entry: len = %d", p.Len())
	}
	if _, ok := p.Timeout(slot, 1); !ok {
		t.Fatal("matching timer did not consume the entry")
	}
}

func TestPendingBounded(t *testing.T) {
	p := NewPending(2)
	if _, _, ok := p.Bind(0, 1, 1, 1, func(Ack) {}); !ok {
		t.Fatal("bind 1")
	}
	if _, _, ok := p.Bind(0, 2, 1, 1, func(Ack) {}); !ok {
		t.Fatal("bind 2")
	}
	if _, _, ok := p.Bind(0, 3, 1, 1, func(Ack) {}); ok {
		t.Fatal("bind past capacity succeeded")
	}
}
