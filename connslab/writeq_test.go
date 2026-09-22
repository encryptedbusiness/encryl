package connslab

import "testing"

func TestFrameRefcount(t *testing.T) {
	f := NewFrame(16)
	if f.Refs() != 1 {
		t.Fatalf("refs = %d, want 1", f.Refs())
	}
	f.Retain()
	f.Retain()
	if f.Refs() != 3 {
		t.Fatalf("refs = %d, want 3", f.Refs())
	}
	f.Release()
	f.Release()
	if f.Refs() != 1 {
		t.Fatalf("refs = %d, want 1", f.Refs())
	}
}

func TestFramePoolReuse(t *testing.T) {
	f := NewFrame(64)
	f.Buf[0] = 0xaa
	f.Release()
	g := NewFrame(64)
	if len(g.Buf) != 64 {
		t.Fatalf("len = %d", len(g.Buf))
	}
	if g.Refs() != 1 {
		t.Fatalf("refs = %d", g.Refs())
	}
	g.Release()
}

func TestNodeQueueFIFO(t *testing.T) {
	var sl Slot
	pool := &NodePool{}
	a := NewFrame(4)
	b := NewFrame(4)
	na := pool.Get()
	na.Frame = a
	nb := pool.Get()
	nb.Frame = b
	sl.Push(na)
	sl.Push(nb)
	got := sl.Pop()
	if got == nil || got.Frame != a {
		t.Fatal("FIFO violated")
	}
	pool.Put(got)
	got = sl.Pop()
	if got == nil || got.Frame != b {
		t.Fatal("FIFO violated")
	}
	pool.Put(got)
	if !sl.QueueEmpty() {
		t.Fatalf("queue not empty")
	}
	a.Release()
	b.Release()
}

func TestAccountingReject(t *testing.T) {
	var a Accounting
	if got := a.Admit(100, 2, 1000, BackpressureReject); got != AdmitQueued {
		t.Fatalf("admit 1 = %v", got)
	}
	if got := a.Admit(100, 2, 1000, BackpressureReject); got != AdmitQueued {
		t.Fatalf("admit 2 = %v", got)
	}
	if got := a.Admit(100, 2, 1000, BackpressureReject); got != AdmitRejected {
		t.Fatalf("admit 3 = %v, want rejected", got)
	}
	a.Release(100)
	if got := a.Admit(100, 2, 1000, BackpressureReject); got != AdmitQueued {
		t.Fatalf("admit after release = %v", got)
	}
}

func TestAccountingDropNewest(t *testing.T) {
	var a Accounting
	if got := a.Admit(100, 1, 1000, BackpressureDropNewest); got != AdmitQueued {
		t.Fatalf("admit 1 = %v", got)
	}
	if got := a.Admit(100, 1, 1000, BackpressureDropNewest); got != AdmitDropped {
		t.Fatalf("admit 2 = %v, want dropped", got)
	}
}

func TestAccountingDropOldest(t *testing.T) {
	var a Accounting
	if got := a.Admit(100, 1, 1000, BackpressureDropOldest); got != AdmitQueued {
		t.Fatalf("admit 1 = %v", got)
	}
	if got := a.Admit(100, 1, 1000, BackpressureDropOldest); got != AdmitQueued {
		t.Fatalf("admit 2 = %v", got)
	}
	if got := a.Admit(100, 1, 1000, BackpressureDropOldest); got != AdmitDropped {
		t.Fatalf("admit 3 = %v, want dropped while an eviction is pending", got)
	}
	if !a.TakeEvict() {
		t.Fatal("eviction not requested")
	}
	if a.TakeEvict() {
		t.Fatal("eviction request was not cleared")
	}
}

func TestAccountingCloseSlow(t *testing.T) {
	var a Accounting
	_ = a.Admit(100, 1, 1000, BackpressureCloseSlow)
	if got := a.Admit(100, 1, 1000, BackpressureCloseSlow); got != AdmitCloseSlow {
		t.Fatalf("got %v", got)
	}
}

func TestAccountingReleaseFloor(t *testing.T) {
	var a Accounting
	a.Release(500)
	m, b := a.Pending()
	if m != 0 || b != 0 {
		t.Fatalf("pending = %d/%d", m, b)
	}
}
