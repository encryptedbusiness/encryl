package reactor

import "testing"

func TestAttachRing(t *testing.T) {
	r := NewAttachRing(2)
	if !r.Push(AttachCommand{FD: 3}) || !r.Push(AttachCommand{FD: 4}) {
		t.Fatal("push within capacity failed")
	}
	if r.Push(AttachCommand{FD: 5}) {
		t.Fatal("full ring accepted a command")
	}
	first, ok := r.Pop()
	if !ok || first.FD != 3 {
		t.Fatalf("first pop = %+v, %v", first, ok)
	}
	second, ok := r.Pop()
	if !ok || second.FD != 4 {
		t.Fatalf("second pop = %+v, %v", second, ok)
	}
	if _, ok := r.Pop(); ok {
		t.Fatal("empty ring returned a command")
	}
}
