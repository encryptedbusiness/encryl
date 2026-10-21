//go:build linux

package event_bench

import (
	"testing"

	encryl "github.com/encryptedbusiness/encryl"
)

func BenchmarkEventIDForName(b *testing.B) {
	for _, name := range []string{"chat", "chat.message", "room:presence:update"} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = encryl.EventIDForName(name)
			}
		})
	}
}
