package fnv

import (
	"bytes"
	"testing"
)

func TestHash32KnownVectors(t *testing.T) {
	// FNV-1a 32-bit reference values.
	tests := []struct {
		in   string
		want uint32
	}{
		{"", 0x811c9dc5},
		{"a", 0xe40c292c},
		{"b", 0xe70c2de5},
		{"foobar", 0xbf9cf968},
	}
	for _, tt := range tests {
		if got := Hash32(tt.in); got != tt.want {
			t.Errorf("Hash32(%q) = %#x, want %#x", tt.in, got, tt.want)
		}
	}
}

func TestHash32MatchesHash32Bytes(t *testing.T) {
	inputs := []string{
		"",
		"a",
		"chat",
		"chat.message",
		"room:presence:update",
		"a longer event name with \x00 embedded bytes",
		"चिटो",
	}
	for _, in := range inputs {
		if got, want := Hash32(in), Hash32Bytes([]byte(in)); got != want {
			t.Errorf("Hash32(%q) = %#x, Hash32Bytes = %#x", in, got, want)
		}
	}
}

func TestHash32IsCaseSensitiveAndOrdered(t *testing.T) {
	if Hash32("Chat") == Hash32("chat") {
		t.Fatal("case-insensitive collision")
	}
	if Hash32("ab") == Hash32("ba") {
		t.Fatal("order-insensitive collision")
	}
}

func TestHash32HandlesBinary(t *testing.T) {
	a := []byte{0x00, 0xff, 0x80, 0x01}
	b := []byte{0x00, 0xff, 0x80, 0x02}
	if Hash32Bytes(a) == Hash32Bytes(b) {
		t.Fatal("distinct binary inputs collided")
	}
	if Hash32Bytes(a) != Hash32Bytes(bytes.Clone(a)) {
		t.Fatal("hash is not deterministic")
	}
}

func TestHash32DoesNotAllocate(t *testing.T) {
	if allocs := testing.AllocsPerRun(1000, func() { _ = Hash32("room:presence:update") }); allocs != 0 {
		t.Fatalf("Hash32 allocated %.2f times", allocs)
	}
	if allocs := testing.AllocsPerRun(1000, func() { _ = Hash32Bytes([]byte("room:presence:update")) }); allocs != 0 {
		t.Fatalf("Hash32Bytes allocated %.2f times", allocs)
	}
}
