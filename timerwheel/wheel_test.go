package timerwheel

import "testing"

func mustAcquire(t *testing.T, w *Wheel, owner uint32, kind uint8) uint32 {
	t.Helper()
	h, ok := w.Acquire()
	if !ok {
		t.Fatal("Acquire failed")
	}
	w.SetMeta(h, owner, kind)
	return h
}

func TestWheelFiresAtDeadline(t *testing.T) {
	w := New(0, 0)
	h := mustAcquire(t, w, 7, 1)
	w.Add(h, 0, 100)

	if out := w.Advance(90, nil); len(out) != 0 {
		t.Fatalf("fired early: %v", out)
	}
	out := w.Advance(100, nil)
	if len(out) != 1 || out[0] != h {
		t.Fatalf("Advance(100) = %v, want [%d]", out, h)
	}
	if w.Node(h).Owner != 7 || w.Node(h).Kind != 1 {
		t.Fatal("node metadata lost")
	}
}

func TestWheelTenSecondPrecision(t *testing.T) {
	w := New(0, 0)
	h := mustAcquire(t, w, 1, 0)
	w.Add(h, 0, 10_000)

	for now := uint64(10); now <= 10_000; now += 10 {
		out := w.Advance(now, nil)
		if len(out) == 0 {
			continue
		}
		if now != 10_000 {
			t.Fatalf("fired at %d ms, want 10000", now)
		}
		if len(out) != 1 || out[0] != h {
			t.Fatalf("out = %v, want [%d]", out, h)
		}
		return
	}
	t.Fatal("10 s deadline never fired")
}

func TestWheelTwoMinutePrecision(t *testing.T) {
	w := New(0, 0)
	h := mustAcquire(t, w, 1, 0)
	w.Add(h, 0, 120_000)

	for now := uint64(10); now <= 120_000; now += 10 {
		out := w.Advance(now, nil)
		if len(out) == 0 {
			continue
		}
		if now != 120_000 {
			t.Fatalf("fired at %d ms, want 120000", now)
		}
		if len(out) != 1 || out[0] != h {
			t.Fatalf("out = %v, want [%d]", out, h)
		}
		return
	}
	t.Fatal("2 minute deadline never fired")
}

func TestWheelRemove(t *testing.T) {
	w := New(0, 0)
	h := mustAcquire(t, w, 1, 0)
	w.Add(h, 0, 100)
	w.Remove(h)
	if out := w.Advance(200, nil); len(out) != 0 {
		t.Fatalf("removed node fired: %v", out)
	}
	if w.Node(h).Linked {
		t.Fatal("node still linked after Remove")
	}
}

func TestWheelRearm(t *testing.T) {
	w := New(0, 0)
	h := mustAcquire(t, w, 1, 0)
	w.Add(h, 0, 100)
	w.Add(h, 0, 500)

	if out := w.Advance(100, nil); len(out) != 0 {
		t.Fatalf("old deadline fired: %v", out)
	}
	if out := w.Advance(500, nil); len(out) != 1 {
		t.Fatalf("new deadline did not fire: %v", out)
	}
}

func TestWheelLongJumpRebuild(t *testing.T) {
	w := New(0, 0)
	h := mustAcquire(t, w, 1, 0)
	w.Add(h, 0, 10_000)

	out := w.Advance(20_000, nil)
	if len(out) != 1 || out[0] != h {
		t.Fatalf("Advance(20000) = %v, want [%d]", out, h)
	}
}

func TestWheelManyNodesSameBucket(t *testing.T) {
	w := New(0, 0)
	var want []uint32
	for i := 0; i < 10; i++ {
		h := mustAcquire(t, w, uint32(i), 0)
		w.Add(h, 0, 100+uint64(i))
		want = append(want, h)
	}
	out := w.Advance(200, nil)
	if len(out) != len(want) {
		t.Fatalf("fired %d nodes, want %d", len(out), len(want))
	}
	seen := make(map[uint32]bool, len(out))
	for _, h := range out {
		seen[h] = true
	}
	for _, h := range want {
		if !seen[h] {
			t.Fatalf("node %d did not fire", h)
		}
	}
}

func TestWheelReleaseAndReacquire(t *testing.T) {
	w := New(0, 0)
	h := mustAcquire(t, w, 1, 0)
	w.Add(h, 0, 100)
	w.Release(h)
	if w.Node(h).Linked {
		t.Fatal("released node still linked")
	}
	h2 := mustAcquire(t, w, 2, 0)
	if h2 != h {
		t.Fatalf("reacquired handle = %d, want %d", h2, h)
	}
}

func TestWheelMetadata(t *testing.T) {
	w := New(0, 0)
	h := mustAcquire(t, w, 99, 7)
	if n := w.Node(h); n.Owner != 99 || n.Kind != 7 {
		t.Fatalf("node = %+v", n)
	}
	if w.Node(NoNode) != nil {
		t.Fatal("Node(NoNode) returned a value")
	}
}

func TestWheelAcquireDoesNotAllocate(t *testing.T) {
	w := New(0, 0)
	h := mustAcquire(t, w, 1, 0)
	w.Add(h, 0, 100)
	w.Remove(h)

	allocs := testing.AllocsPerRun(1000, func() {
		w.Add(h, 0, 100)
		w.Remove(h)
	})
	if allocs != 0 {
		t.Fatalf("Add/Remove = %.2f allocs, want 0", allocs)
	}
}
