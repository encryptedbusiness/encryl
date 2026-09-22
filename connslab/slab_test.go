package connslab

import (
	"testing"

	"github.com/encryptedbusiness/encryl/timerwheel"
)

func TestSlabAcquireReleaseAndGeneration(t *testing.T) {
	s := NewSlab(100, 8)
	slot, gen, ok := s.Acquire(7)
	if !ok {
		t.Fatal("Acquire failed")
	}
	if sl, ok := s.Get(slot, gen); !ok || sl.FD != 7 {
		t.Fatalf("Get = %+v, %v", sl, ok)
	}
	if !s.Transition(slot, StateClosed) {
		t.Fatal("Accepted -> Closed must be legal")
	}
	if _, ok := s.Release(slot); !ok {
		t.Fatal("Release failed")
	}
	if _, ok := s.Get(slot, gen); ok {
		t.Fatal("stale generation accepted")
	}
	slot2, gen2, ok := s.Acquire(8)
	if !ok || slot2 != slot {
		t.Fatalf("reuse = %d/%d, want slot %d", slot2, gen2, slot)
	}
	if gen2 == gen {
		t.Fatal("generation did not advance on reuse")
	}
	if s.Live() != 1 {
		t.Fatalf("Live = %d, want 1", s.Live())
	}
}

func TestSlabInFlightPreventsRelease(t *testing.T) {
	s := NewSlab(10, 4)
	slot, _, _ := s.Acquire(3)
	if !s.BeginOp(slot) {
		t.Fatal("first BeginOp failed")
	}
	if !s.BeginOp(slot) {
		t.Fatal("second BeginOp failed")
	}
	s.Transition(slot, StateClosed)
	if _, ok := s.Release(slot); ok {
		t.Fatal("Release succeeded with operations in flight")
	}
	if sl, ok := s.GetAny(slot); ok {
		sl.InFlight = 0
	}
	if _, ok := s.Release(slot); !ok {
		t.Fatal("Release failed after operations completed")
	}
}

func TestSlabRequiresClosedState(t *testing.T) {
	s := NewSlab(10, 4)
	slot, _, _ := s.Acquire(3)
	if _, ok := s.Release(slot); ok {
		t.Fatal("Release succeeded on an accepted slot")
	}
	if s.Transition(slot, StateOpen) {
		t.Fatal("Accepted -> Open must be illegal")
	}
}

func TestSlabStateMachine(t *testing.T) {
	legal := []struct {
		from, to State
	}{
		{StateAccepted, StateHandshaking},
		{StateHandshaking, StateOpen},
		{StateHandshaking, StateAuthenticating},
		{StateAuthenticating, StateOpen},
		{StateOpen, StateDraining},
		{StateDraining, StateClosing},
		{StateClosing, StateClosed},
		{StateOpen, StateClosed},
	}
	for _, tc := range legal {
		s := NewSlab(4, 2)
		slot, _, _ := s.Acquire(1)
		sl, _ := s.GetAny(slot)
		sl.State = tc.from
		if !s.Transition(slot, tc.to) {
			t.Errorf("%s -> %s rejected", tc.from, tc.to)
		}
	}
}

func TestSlabChunkGrowth(t *testing.T) {
	s := NewSlab(100, 4)
	for i := 0; i < 20; i++ {
		if _, _, ok := s.Acquire(i); !ok {
			t.Fatalf("Acquire %d failed", i)
		}
	}
	if len(s.chunks) != 5 {
		t.Fatalf("chunks = %d, want 5", len(s.chunks))
	}
	if s.Live() != 20 {
		t.Fatalf("Live = %d, want 20", s.Live())
	}
}

func TestSlabPeer(t *testing.T) {
	s := NewSlab(4, 2)
	slot, _, _ := s.Acquire(1)
	s.SetPeer(slot, []byte{127, 0, 0, 1}, 8080, 4)
	ip, port, family, ok := s.Peer(slot)
	if !ok || port != 8080 || family != 4 || ip[0] != 127 || ip[3] != 1 {
		t.Fatalf("Peer = %v %d %d %v", ip, port, family, ok)
	}
}

func TestSlabTimers(t *testing.T) {
	s := NewSlab(4, 2)
	slot, _, _ := s.Acquire(1)
	if h, _ := s.Timer(slot, TimerHandshake); h != timerwheel.NoNode {
		t.Fatalf("initial timer = %d, want NoNode", h)
	}
	s.SetTimer(slot, TimerHandshake, 9)
	if h, _ := s.Timer(slot, TimerHandshake); h != 9 {
		t.Fatalf("timer = %d, want 9", h)
	}
	s.ClearTimer(slot, TimerHandshake)
	if h, _ := s.Timer(slot, TimerHandshake); h != timerwheel.NoNode {
		t.Fatalf("cleared timer = %d, want NoNode", h)
	}
}

func TestSlabAcquireReleaseDoesNotAllocate(t *testing.T) {
	s := NewSlab(1024, 1024)
	// Warm the free list and every slot.
	for i := 0; i < 10; i++ {
		slot, _, _ := s.Acquire(i)
		s.Transition(slot, StateClosed)
		s.Release(slot)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		slot, _, ok := s.Acquire(1)
		if !ok {
			t.Fatal("Acquire failed")
		}
		s.Transition(slot, StateClosed)
		s.Release(slot)
	})
	if allocs != 0 {
		t.Fatalf("Acquire/Release = %.2f allocs, want 0", allocs)
	}
}
