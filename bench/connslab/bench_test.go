package connslab_bench

import (
	"testing"

	"github.com/encryptedbusiness/encryl/connslab"
)

func BenchmarkAccountingAdmit(b *testing.B) {
	var a connslab.Accounting
	const cost = 64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if a.Admit(cost, 1<<20, 1<<30, connslab.BackpressureReject) == connslab.AdmitQueued {
			a.Release(cost)
		}
	}
}

func BenchmarkFrameAcquireRelease(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := connslab.NewFrame(64)
		f.Release()
	}
}

func BenchmarkQueuePushPop(b *testing.B) {
	var s connslab.Slot
	nodes := make([]*connslab.Node, 64)
	frames := make([]*connslab.Frame, 64)
	for i := range nodes {
		frames[i] = connslab.NewFrame(64)
		nodes[i] = &connslab.Node{Frame: frames[i]}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := nodes[i%len(nodes)]
		s.Push(n)
		_ = s.Pop()
	}
	b.StopTimer()
	for _, f := range frames {
		f.Release()
	}
}
