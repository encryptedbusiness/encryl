package room

import (
	"testing"

	connection "github.com/encryptedbusiness/encryl/connslab"
)

func TestPlannerGroupsByLoop(t *testing.T) {
	p := AcquirePlanner()
	p.Add(connection.Ref{Loop: 0, Slot: 1, Gen: 1})
	p.Add(connection.Ref{Loop: 1, Slot: 2, Gen: 1})
	p.Add(connection.Ref{Loop: 0, Slot: 3, Gen: 1})
	groups := p.Groups()
	if len(groups) != 2 {
		t.Fatalf("groups = %d", len(groups))
	}
	if len(groups[0].Refs) != 2 || groups[0].Loop != 0 {
		t.Fatalf("group 0 = %+v", groups[0])
	}
	if len(groups[1].Refs) != 1 || groups[1].Loop != 1 {
		t.Fatalf("group 1 = %+v", groups[1])
	}
	p.Release()

	// The pool must hand back a reset planner.
	p = AcquirePlanner()
	if !p.Empty() {
		t.Fatal("planner not reset")
	}
	p.Add(connection.Ref{Loop: 3, Slot: 9, Gen: 1})
	if groups := p.Groups(); len(groups) != 1 || len(groups[0].Refs) != 1 {
		t.Fatalf("reused planner groups = %+v", groups)
	}
	p.Release()

	// Regression: a reused planner must still group a loop whose map entry
	// survived the previous use.
	p = AcquirePlanner()
	p.Add(connection.Ref{Loop: 0, Slot: 1, Gen: 1})
	p.Add(connection.Ref{Loop: 0, Slot: 2, Gen: 1})
	if groups := p.Groups(); len(groups) != 1 || len(groups[0].Refs) != 2 {
		t.Fatalf("same-loop reuse groups = %+v", groups)
	}
	p.Release()
}

func TestSnapshotIntoReusesSlice(t *testing.T) {
	d := New(0)
	a := connection.Ref{Loop: 0, Slot: 1, Gen: 1}
	b := connection.Ref{Loop: 0, Slot: 2, Gen: 1}
	_ = d.Join("x", a)
	_ = d.Join("x", b)

	buf := make([]connection.Ref, 0, 4)
	buf = d.SnapshotInto("x", buf)
	if len(buf) != 2 {
		t.Fatalf("len = %d", len(buf))
	}
	for _, ref := range buf {
		if ref != a && ref != b {
			t.Fatalf("unexpected ref %+v", ref)
		}
	}
	if d.Size("x") != 2 {
		t.Fatalf("size = %d", d.Size("x"))
	}
}
