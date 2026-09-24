package room

import (
	"testing"

	connection "github.com/encryptedbusiness/encryl/connslab"
)

func TestSelfRefRoundTrip(t *testing.T) {
	const perLoop = 8
	for _, id := range []connection.ID{
		{Slot: 0, Gen: 1},
		{Slot: 7, Gen: 9},
		{Slot: 8, Gen: 2},
		{Slot: 8*3 + 5, Gen: 11},
	} {
		ref, ok := SelfRef(id, perLoop)
		if !ok {
			t.Fatalf("SelfRef(%+v) failed", id)
		}
		if got := uint32(ref.Loop)*perLoop + ref.Slot; got != id.Slot {
			t.Fatalf("round trip = %d, want %d", got, id.Slot)
		}
		if ref.Gen != id.Gen {
			t.Fatalf("gen = %d, want %d", ref.Gen, id.Gen)
		}
		if ref.Slot != id.Slot%perLoop {
			t.Fatalf("local slot = %d", ref.Slot)
		}
	}
}

func TestSelfRefRejectsInvalid(t *testing.T) {
	if _, ok := SelfRef(connection.ID{Slot: 1, Gen: 0}, 8); ok {
		t.Fatal("zero generation accepted")
	}
	if _, ok := SelfRef(connection.ID{Slot: 1, Gen: 1}, 0); ok {
		t.Fatal("zero perLoop accepted")
	}
}

func TestSelfRoomCostsNoStorage(t *testing.T) {
	d := New(0)
	ref, ok := SelfRef(connection.ID{Slot: 42, Gen: 3}, 16)
	if !ok {
		t.Fatal("resolve failed")
	}
	if d.Size("42") != 0 {
		t.Fatal("self-room materialized in the directory")
	}
	if rooms, memberships := d.Counts(); rooms != 0 || memberships != 0 {
		t.Fatalf("counts = %d/%d", rooms, memberships)
	}
	if ref.Slot != 42%16 {
		t.Fatalf("local slot = %d", ref.Slot)
	}
}
