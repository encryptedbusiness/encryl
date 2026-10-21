package room_bench

import (
	"testing"

	"github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/room"
)

func BenchmarkSnapshot1000(b *testing.B) {
	d := room.New(0)
	for i := 0; i < 1000; i++ {
		_ = d.Join("room", room.Ref{Loop: uint16(i % 4), Slot: uint32(i), Gen: 1})
	}
	buf := make([]room.Ref, 0, 1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = d.SnapshotInto("room", buf[:0])
		if len(buf) != 1000 {
			b.Fatalf("snapshot = %d", len(buf))
		}
	}
}

func BenchmarkPlannerBuild(b *testing.B) {
	refs := make([]connslab.Ref, 4096)
	for i := range refs {
		refs[i] = connslab.Ref{Loop: uint16(i % 8), Slot: uint32(i), Gen: 1}
	}
	p := room.AcquirePlanner()
	// Warm the planner's map and group capacities before timing.
	p.Reset()
	for _, ref := range refs {
		p.Add(ref)
	}
	_ = p.Groups()
	p.Reset()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Reset()
		for _, ref := range refs {
			p.Add(ref)
		}
		_ = p.Groups()
	}
	b.StopTimer()
	p.Release()
}
