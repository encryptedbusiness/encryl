package timerwheel_bench

import (
	"testing"

	"github.com/encryptedbusiness/encryl/timerwheel"
)

func BenchmarkAddRemove(b *testing.B) {
	w := timerwheel.New(0, 0)
	h, _ := w.Acquire()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Add(h, 0, 100)
		w.Remove(h)
	}
}
