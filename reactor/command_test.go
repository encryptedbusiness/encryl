package reactor

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestCommandRingFIFO(t *testing.T) {
	r := NewCommandRing(4)
	for i := 0; i < 3; i++ {
		if !r.Push(Command{Kind: CmdSend, Slot: uint32(i)}) {
			t.Fatalf("Push %d failed", i)
		}
	}
	for i := 0; i < 3; i++ {
		c, ok := r.Pop()
		if !ok || c.Slot != uint32(i) {
			t.Fatalf("Pop %d = %+v, %v", i, c, ok)
		}
	}
	if _, ok := r.Pop(); ok {
		t.Fatal("Pop on an empty ring succeeded")
	}
}

func TestCommandRingBackpressure(t *testing.T) {
	r := NewCommandRing(2)
	c := Command{Kind: CmdSend}
	if !r.Push(c) {
		t.Fatal("Push failed below capacity")
	}
	if !r.Push(c) {
		t.Fatal("Push failed below capacity")
	}
	if r.Push(Command{Kind: CmdSend}) {
		t.Fatal("Push succeeded past capacity")
	}
	if r.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", r.Dropped())
	}
}

func TestCommandRingWakeOnce(t *testing.T) {
	r := NewCommandRing(4)
	var wake atomic.Int64
	r.SetWake(func() { wake.Add(1) })

	r.Push(Command{Kind: CmdSend})
	r.Push(Command{Kind: CmdSend})
	r.Push(Command{Kind: CmdSend})
	if got := wake.Load(); got != 1 {
		t.Fatalf("wake count = %d, want 1", got)
	}
	r.Pop()
	r.Pop()
	r.Pop()
	r.Push(Command{Kind: CmdSend})
	if got := wake.Load(); got != 2 {
		t.Fatalf("wake count = %d, want 2", got)
	}
}

func TestCommandRingConcurrentProducers(t *testing.T) {
	const producers = 8
	const perProducer = 2000
	r := NewCommandRing(producers * perProducer)
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				if !r.Push(Command{Kind: CmdSend, Slot: uint32(p)}) {
					t.Errorf("producer %d: ring full", p)
					return
				}
			}
		}(p)
	}
	wg.Wait()
	if r.Len() != producers*perProducer {
		t.Fatalf("Len = %d, want %d", r.Len(), producers*perProducer)
	}
	for i := 0; i < producers*perProducer; i++ {
		if _, ok := r.Pop(); !ok {
			t.Fatalf("Pop %d failed", i)
		}
	}
}
