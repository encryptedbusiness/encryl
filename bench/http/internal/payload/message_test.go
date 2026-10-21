package payload

import "testing"

func TestAppendMessage(t *testing.T) {
	var buf [32]byte
	body := AppendMessage(buf[:0], Message{Message: "Hello, World!"})
	if string(body) != `{"message":"Hello, World!"}` {
		t.Fatalf("body = %q", body)
	}
	if n := testing.AllocsPerRun(1000, func() {
		_ = AppendMessage(buf[:0], Message{Message: "Hello, World!"})
	}); n != 0 {
		t.Fatalf("AppendMessage allocs = %v, want 0", n)
	}
}
