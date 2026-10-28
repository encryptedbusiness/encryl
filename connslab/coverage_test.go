package connslab

import (
	"testing"

	"github.com/encryptedbusiness/encryl/event"
	upgrade "github.com/encryptedbusiness/encryl/httpx"
	"github.com/encryptedbusiness/encryl/timerwheel"
	"github.com/encryptedbusiness/encryl/ws"
)

func TestStateAndTimerKindStrings(t *testing.T) {
	states := map[State]string{
		StateFree:           "free",
		StateAccepted:       "accepted",
		StateHandshaking:    "handshaking",
		StateAuthenticating: "authenticating",
		StateOpen:           "open",
		StateDraining:       "draining",
		StateClosing:        "closing",
		StateClosed:         "closed",
	}
	for s, want := range states {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
	if got := State(200).String(); got != "unknown" {
		t.Errorf("unknown State = %q", got)
	}

	kinds := map[TimerKind]string{
		TimerHandshake: "handshake",
		TimerIdle:      "idle",
		TimerPing:      "ping",
		TimerClose:     "close",
	}
	for k, want := range kinds {
		if got := k.String(); got != want {
			t.Errorf("TimerKind(%d).String() = %q, want %q", k, got, want)
		}
	}
	if got := TimerKind(200).String(); got != "unknown" {
		t.Errorf("unknown TimerKind = %q", got)
	}
}

func TestSlotExtraLifecycle(t *testing.T) {
	var sl Slot
	if sl.Handshake() != nil || sl.Acct() != nil || sl.Pending() != nil {
		t.Fatal("zero slot must have no extras")
	}

	p := &upgrade.Parser{}
	sl.SetHandshake(p)
	if sl.Handshake() != p {
		t.Fatal("handshake not installed")
	}

	acct := &Accounting{}
	sl.SetAcct(acct)
	if sl.Acct() != acct {
		t.Fatal("accounting not installed")
	}

	sl.SetPending([]byte("abc"))
	if got := string(sl.Pending()); got != "abc" {
		t.Fatalf("Pending = %q", got)
	}
	if sl.extra.owned {
		t.Fatal("SetPending must not claim ownership")
	}
	b, owned := sl.TakePending()
	if string(b) != "abc" || owned {
		t.Fatalf("TakePending = %q/%v", b, owned)
	}
	if sl.Pending() != nil {
		t.Fatal("pending survived TakePending")
	}

	sl.SetPendingOwned([]byte("owned"))
	b, owned = sl.TakePending()
	if string(b) != "owned" || !owned {
		t.Fatalf("owned TakePending = %q/%v", b, owned)
	}

	sl.SetHandshake(nil)
	sl.SetAcct(nil)
	if sl.extra != nil {
		t.Fatal("extra not dropped once idle")
	}

	// Clearing an absent extra must not allocate or panic.
	sl.SetHandshake(nil)
	sl.SetAcct(nil)
	sl.SetPending(nil)
}

func TestSlotFlags(t *testing.T) {
	var sl Slot
	if sl.Has(FlagCloseQueued) {
		t.Fatal("unexpected flag")
	}
	sl.Set(FlagCloseQueued | FlagPollArmed)
	if !sl.Has(FlagCloseQueued) || !sl.Has(FlagPollArmed) {
		t.Fatal("Set did not apply flags")
	}
	sl.SetTo(FlagCloseQueued, false)
	if sl.Has(FlagCloseQueued) || !sl.Has(FlagPollArmed) {
		t.Fatal("SetTo(false) did not clear only the target flag")
	}
	sl.Clear(FlagPollArmed)
	if sl.Has(FlagPollArmed) {
		t.Fatal("Clear did not clear")
	}
}

func TestSlabLimitsAndInvalidHandles(t *testing.T) {
	s := NewSlab(2, 2)
	a, ag, ok := s.Acquire(10)
	if !ok {
		t.Fatal("first acquire failed")
	}
	b, bg, ok := s.Acquire(11)
	if !ok {
		t.Fatal("second acquire failed")
	}
	if _, _, ok := s.Acquire(12); ok {
		t.Fatal("acquire above the ceiling succeeded")
	}
	if s.HighWater() != 2 || s.Live() != 2 {
		t.Fatalf("high water/live = %d/%d", s.HighWater(), s.Live())
	}

	if _, ok := s.Get(a, ag+1); ok {
		t.Fatal("wrong generation accepted")
	}
	if _, ok := s.Get(99, 1); ok {
		t.Fatal("slot beyond high water accepted")
	}
	if _, ok := s.GetAny(99); ok {
		t.Fatal("GetAny beyond high water accepted")
	}
	if s.Transition(99, StateClosed) {
		t.Fatal("transition on an invalid slot succeeded")
	}
	if s.Transition(a, State(200)) {
		t.Fatal("transition to an out-of-range state succeeded")
	}
	if s.illegal == 0 {
		t.Fatal("illegal transitions were not counted")
	}

	if s.SetTimer(a, NumTimerKinds, 1) {
		t.Fatal("timer kind out of range accepted")
	}
	if _, ok := s.Timer(a, NumTimerKinds); ok {
		t.Fatal("timer kind out of range returned")
	}
	s.ClearTimer(a, NumTimerKinds)

	// A peer address longer than the compact tuple is truncated, not copied
	// past the array.
	s.SetPeer(a, make([]byte, 32), 1234, 6)
	ip, port, family, ok := s.Peer(a)
	if !ok || port != 1234 || family != 6 || ip[15] != 0 {
		t.Fatalf("peer = %v/%d/%d/%v", ip, port, family, ok)
	}
	if _, _, _, ok := s.Peer(99); ok {
		t.Fatal("peer for an invalid slot reported ok")
	}

	// A slot with no peer address reports false.
	if _, _, _, ok := s.Peer(b); ok {
		t.Fatal("peer without an address reported ok")
	}
	_ = bg
}

func TestNewSlabClampsArguments(t *testing.T) {
	s := NewSlab(-1, -1)
	if s.max != 1 || s.chunkSize != 1 {
		t.Fatalf("clamped slab = max %d chunk %d", s.max, s.chunkSize)
	}
	if _, _, ok := s.Acquire(1); !ok {
		t.Fatal("acquire on the clamped slab failed")
	}
	if _, _, ok := s.Acquire(2); ok {
		t.Fatal("clamped slab exceeded its ceiling")
	}

	big := NewSlab(int(SlotLimit)+100, 0)
	if big.max != SlotLimit {
		t.Fatalf("max = %d, want %d", big.max, SlotLimit)
	}
	if big.chunkSize != 4096 {
		t.Fatalf("chunk size = %d", big.chunkSize)
	}
}

func TestBatchPool(t *testing.T) {
	b := AcquireBatch(8)
	if cap(b.Refs) < 8 {
		t.Fatalf("capacity = %d", cap(b.Refs))
	}
	b.Refs = append(b.Refs, Ref{Loop: 1, Slot: 2, Gen: 3})
	b.Release()

	// Reuse keeps the backing array and clears the length.
	b2 := AcquireBatch(8)
	if len(b2.Refs) != 0 || cap(b2.Refs) < 8 {
		t.Fatalf("reused batch = len %d cap %d", len(b2.Refs), cap(b2.Refs))
	}
	b2.Release()

	var nilBatch *Batch
	nilBatch.Release()
}

func TestBufferClassBoundaries(t *testing.T) {
	tests := []struct {
		n    int
		want int
	}{
		{0, 0}, {1, 0}, {256, 0}, {257, 1}, {1024, 1}, {1025, 2},
		{4096, 2}, {4097, 3}, {16384, 3}, {16385, 4}, {65536, 4},
		{65537, -1},
	}
	for _, tt := range tests {
		if got := bufferClass(tt.n); got != tt.want {
			t.Errorf("bufferClass(%d) = %d, want %d", tt.n, got, tt.want)
		}
	}

	// Oversized buffers are not pooled, but still round-trip through a Frame.
	f := NewFrame(65537)
	if f.class != -1 {
		t.Fatalf("class = %d, want -1", f.class)
	}
	f.Buf[0] = 1
	f.Release()
}

func TestAccountingAddAndReset(t *testing.T) {
	var a Accounting
	a.Add(100)
	a.Add(50)
	if msgs, bytes := a.Pending(); msgs != 2 || bytes != 150 {
		t.Fatalf("pending = %d/%d", msgs, bytes)
	}
	if a.TakeEvict() {
		t.Fatal("unexpected eviction request")
	}
	a.Reset()
	if msgs, bytes := a.Pending(); msgs != 0 || bytes != 0 {
		t.Fatalf("reset pending = %d/%d", msgs, bytes)
	}
}

func TestBuildFrameEncodesEnvelopeAndPayload(t *testing.T) {
	payload := []byte("hello")
	f := BuildFrame(event.KindEvent, 1024, 42, 0, payload)
	defer f.Release()

	body := event.HeaderSize + len(payload)
	header := ws.FrameHeaderLen(body)
	if len(f.Buf) != header+body {
		t.Fatalf("frame length = %d, want %d", len(f.Buf), header+body)
	}
	if f.Buf[0] != 0x82 {
		t.Fatalf("first byte = %#x, want a final binary frame", f.Buf[0])
	}
	if !f.Counted || f.Cost != int32(len(f.Buf)) {
		t.Fatalf("counted/cost = %v/%d", f.Counted, f.Cost)
	}

	env, err := event.Decode(f.Buf[header : header+event.HeaderSize])
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if env.Kind != event.KindEvent || env.Event != 1024 || env.RequestID != 42 {
		t.Fatalf("envelope = %+v", env)
	}
	if got := string(f.Buf[header+event.HeaderSize:]); got != "hello" {
		t.Fatalf("payload = %q", got)
	}
}

func TestNewFrameWithHeaderReservesHeader(t *testing.T) {
	f, header := NewFrameWithHeader(10)
	defer f.Release()
	if header != ws.FrameHeaderLen(10) {
		t.Fatalf("header = %d, want %d", header, ws.FrameHeaderLen(10))
	}
	if len(f.Buf) != header+10 {
		t.Fatalf("length = %d, want %d", len(f.Buf), header+10)
	}
	// The reserved header is already a complete binary frame header.
	if f.Buf[0] != 0x82 {
		t.Fatalf("first byte = %#x", f.Buf[0])
	}
}

func TestTimersRoundTripAndClear(t *testing.T) {
	s := NewSlab(4, 2)
	slot, _, _ := s.Acquire(1)

	if h, ok := s.Timer(slot, TimerIdle); !ok || h != timerwheel.NoNode {
		t.Fatalf("initial timer = %d/%v", h, ok)
	}
	if !s.SetTimer(slot, TimerIdle, 77) {
		t.Fatal("SetTimer failed")
	}
	if h, ok := s.Timer(slot, TimerIdle); !ok || h != 77 {
		t.Fatalf("timer = %d/%v", h, ok)
	}
	s.ClearTimer(slot, TimerIdle)
	if h, _ := s.Timer(slot, TimerIdle); h != timerwheel.NoNode {
		t.Fatalf("cleared timer = %d", h)
	}
	if _, ok := s.Timer(99, TimerIdle); ok {
		t.Fatal("timer for an invalid slot reported ok")
	}
	if s.SetTimer(99, TimerIdle, 1) {
		t.Fatal("SetTimer for an invalid slot succeeded")
	}
}
