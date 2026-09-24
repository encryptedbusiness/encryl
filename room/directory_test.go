package room

import (
	"errors"
	"sync"
	"testing"
)

func TestJoinLeaveSnapshot(t *testing.T) {
	d := New(4)
	a := Ref{Loop: 0, Slot: 1, Gen: 1}
	b := Ref{Loop: 0, Slot: 2, Gen: 1}
	if err := d.Join("general", a); err != nil {
		t.Fatal(err)
	}
	if err := d.Join("general", b); err != nil {
		t.Fatal(err)
	}
	if err := d.Join("general", a); err != nil {
		t.Fatalf("duplicate join: %v", err)
	}
	snap := d.SnapshotInto("general", nil)
	if len(snap) != 2 {
		t.Fatalf("snapshot = %v", snap)
	}
	d.Leave("general", a)
	snap = d.SnapshotInto("general", snap[:0])
	if len(snap) != 1 || snap[0] != b {
		t.Fatalf("snapshot after leave = %v", snap)
	}
	d.Leave("general", b)
	if snap := d.SnapshotInto("general", snap[:0]); len(snap) != 0 {
		t.Fatalf("empty room snapshot = %v", snap)
	}
	rooms, memberships := d.Counts()
	if rooms != 0 || memberships != 0 {
		t.Fatalf("counts = %d/%d", rooms, memberships)
	}
}

func TestJoinLimit(t *testing.T) {
	d := New(2)
	ref := Ref{Slot: 1, Gen: 1}
	if err := d.Join("a", ref); err != nil {
		t.Fatal(err)
	}
	if err := d.Join("b", ref); err != nil {
		t.Fatal(err)
	}
	if err := d.Join("c", ref); !errors.Is(err, ErrLimit) {
		t.Fatalf("err = %v, want ErrLimit", err)
	}
}

func TestRemoveConn(t *testing.T) {
	d := New(0)
	ref := Ref{Slot: 7, Gen: 3}
	_ = d.Join("a", ref)
	_ = d.Join("b", ref)
	d.RemoveConn(ref)
	if rooms, memberships := d.Counts(); rooms != 0 || memberships != 0 {
		t.Fatalf("counts = %d/%d", rooms, memberships)
	}
}

func TestConcurrentJoinEmitClose(t *testing.T) {
	d := New(0)
	const workers = 16
	const rooms = 8
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ref := Ref{Loop: uint16(w % 2), Slot: uint32(w), Gen: uint32(w + 1)}
			var scratch []Ref
			for r := 0; r < rooms; r++ {
				name := string(rune('a' + r))
				_ = d.Join(name, ref)
				scratch = d.SnapshotInto(name, scratch[:0])
				d.Leave(name, ref)
			}
		}(w)
	}
	wg.Wait()
	if rooms, memberships := d.Counts(); rooms != 0 || memberships != 0 {
		t.Fatalf("counts after storm = %d/%d", rooms, memberships)
	}
}

func TestSnapshotDoesNotAlias(t *testing.T) {
	d := New(0)
	ref := Ref{Slot: 1, Gen: 1}
	_ = d.Join("a", ref)
	snap := d.SnapshotInto("a", nil)
	snap[0] = Ref{Slot: 99}
	if got := d.SnapshotInto("a", nil); got[0] != ref {
		t.Fatalf("snapshot aliases internal state: %v", got)
	}
}

func TestRoomNamesCallbackMayJoin(t *testing.T) {
	d := New(0)
	ref := Ref{Slot: 1, Gen: 1}
	other := Ref{Slot: 2, Gen: 1}
	_ = d.Join("a", ref)

	// The callback runs without the directory lock, so it may mutate the
	// directory. Holding the lock here would deadlock this test.
	d.RoomNames(ref, func(name string) bool {
		if err := d.Join(name, other); err != nil {
			t.Errorf("Join in callback: %v", err)
		}
		return true
	})
	if n := d.Size("a"); n != 2 {
		t.Fatalf("room size = %d, want 2", n)
	}
}

func TestRoomNamesStops(t *testing.T) {
	d := New(0)
	ref := Ref{Slot: 1, Gen: 1}
	for _, name := range []string{"a", "b", "c"} {
		_ = d.Join(name, ref)
	}
	seen := 0
	d.RoomNames(ref, func(string) bool {
		seen++
		return false
	})
	if seen != 1 {
		t.Fatalf("callback ran %d times, want 1", seen)
	}
}
