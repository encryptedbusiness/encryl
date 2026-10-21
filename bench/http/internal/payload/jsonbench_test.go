package payload

import (
	stdjson "encoding/json"
	"testing"

	goccyjson "github.com/goccy/go-json"
)

var benchMessage = Message{Message: "Hello, World!"}

func BenchmarkStdMarshal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = stdjson.Marshal(benchMessage)
	}
}

func BenchmarkGoccyMarshal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = goccyjson.Marshal(benchMessage)
	}
}

func BenchmarkAppendMessage(b *testing.B) {
	var buf [32]byte
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = AppendMessage(buf[:0], benchMessage)
	}
}
