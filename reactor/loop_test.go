//go:build linux

package reactor

import (
	"context"
	"errors"
	"github.com/encryptedbusiness/encryl/netio"
	"github.com/encryptedbusiness/encryl/uring"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/timerwheel"
)

// newIdleLoop builds a loop that is never run. It is enough to exercise the
// bookkeeping helpers that do not submit to the ring.
func newIdleLoop(t *testing.T, mutate func(*LoopConfig)) *loop {
	t.Helper()
	ln, err := netio.Listen("127.0.0.1:0", false, 128)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	cfg := LoopConfig{
		Listener:           ln,
		ReadBufferSize:     4096,
		BufferCount:        8,
		TxBytes:            1 << 20,
		MaxConnections:     64,
		RingEntries:        256,
		WriteQueueMessages: 64,
		WriteQueueBytes:    1 << 20,
		IdleTimeout:        time.Minute,
		HandshakeTimeout:   time.Minute,
		ShutdownTimeout:    time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	l, err := NewLoop(cfg)
	if err != nil {
		_ = ln.Close()
		t.Fatalf("NewLoop: %v", err)
	}
	lp := l.(*loop)
	t.Cleanup(func() {
		lp.rx.Close()
		lp.tx.Close()
		_ = ln.Close()
	})
	return lp
}

func TestRingSizesFallback(t *testing.T) {
	cases := []struct {
		entries uint32
		want    []uint32
	}{
		{32768, []uint32{32768, 8192, 4096, 1024, 256, 64}},
		{8192, []uint32{8192, 4096, 1024, 256, 64}},
		{4096, []uint32{4096, 1024, 256, 64}},
		{256, []uint32{256, 64}},
		{64, []uint32{64}},
	}
	for _, tc := range cases {
		got := ringSizes(tc.entries)
		if len(got) != len(tc.want) {
			t.Fatalf("ringSizes(%d) = %v, want %v", tc.entries, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("ringSizes(%d) = %v, want %v", tc.entries, got, tc.want)
			}
		}
	}
}

func TestExcludedRefIncludesLoop(t *testing.T) {
	refs := []connection.Ref{{Loop: 0, Slot: 3, Gen: 7}}
	if !excludedRef(refs, 0, 3, 7) {
		t.Fatal("exact ref was not excluded")
	}
	if excludedRef(refs, 1, 3, 7) {
		t.Fatal("a slot/gen match on another loop was excluded")
	}
}

func TestAckTimerCarriesGeneration(t *testing.T) {
	lp := newIdleLoop(t, nil)
	var gotSlot, gotGen uint32
	lp.cfg.OnAckTimeout = func(slot, gen uint32) {
		gotSlot, gotGen = slot, gen
	}
	if !lp.armAckTimer(5, 77, timerwheel.TickMillis) {
		t.Fatal("armAckTimer failed")
	}
	lp.now = timerwheel.TickMillis
	lp.advanceTimers()
	if gotSlot != 5 || gotGen != 77 {
		t.Fatalf("timeout = slot %d gen %d, want 5/77", gotSlot, gotGen)
	}
}

func TestArmAckTimerReportsExhaustion(t *testing.T) {
	lp := newIdleLoop(t, nil)
	lp.wheel = timerwheel.New(0, 1)
	if !lp.armAckTimer(1, 1, 1000) {
		t.Fatal("first arm failed")
	}
	if lp.armAckTimer(2, 1, 1000) {
		t.Fatal("second arm succeeded with an exhausted wheel")
	}
}

func TestFanoutDropKeepsAccounting(t *testing.T) {
	lp := newIdleLoop(t, func(cfg *LoopConfig) {
		cfg.WriteQueueMessages = 1
		cfg.Backpressure = connection.BackpressureDropNewest
	})
	slot, gen, ok := lp.slab.Acquire(1)
	if !ok {
		t.Fatal("Acquire failed")
	}
	if !lp.slab.Transition(slot, connection.StateHandshaking) || !lp.slab.Transition(slot, connection.StateOpen) {
		t.Fatal("state transition failed")
	}
	sl, _ := lp.slab.Get(slot, gen)
	sl.SetAcct(&connection.Accounting{})
	// One frame already owns the only message slot.
	sl.Acct().Add(16)
	beforeMsgs, beforeBytes := sl.Acct().Pending()

	// A fan-out frame is Counted but not Admitted: dropping it before the
	// loop reserves budget must not release another frame's budget.
	f := connection.NewFrame(16)
	f.Counted = true
	if lp.enqueueOwned(slot, gen, f) {
		t.Fatal("fan-out frame was admitted to a full queue")
	}
	afterMsgs, afterBytes := sl.Acct().Pending()
	if afterMsgs != beforeMsgs || afterBytes != beforeBytes {
		t.Fatalf("accounting changed on drop: %d/%d -> %d/%d", beforeMsgs, beforeBytes, afterMsgs, afterBytes)
	}
}

func TestNoSubmissionQueueDefersCancelAndClose(t *testing.T) {
	lp := newIdleLoop(t, nil)
	lp.ring = nil // getSQE returns nil, as it does when submission fails.

	lp.cancelOp(OpRecv, 1, 1)
	if len(lp.retryCancels) != 1 {
		t.Fatalf("retryCancels = %d, want 1", len(lp.retryCancels))
	}

	slot, gen, ok := lp.slab.Acquire(1)
	if !ok {
		t.Fatal("Acquire failed")
	}
	if !lp.slab.Transition(slot, connection.StateHandshaking) ||
		!lp.slab.Transition(slot, connection.StateOpen) ||
		!lp.slab.Transition(slot, connection.StateClosing) {
		t.Fatal("state transition failed")
	}
	lp.maybeClose(slot, gen)
	if len(lp.retryCloses) != 1 {
		t.Fatalf("retryCloses = %d, want 1", len(lp.retryCloses))
	}
}

func testLoopConfig(t *testing.T, rawEcho bool) (Loop, *loop, context.CancelFunc) {
	t.Helper()
	ln, err := netio.Listen("127.0.0.1:0", false, 1024)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	l, err := NewLoop(LoopConfig{
		Listener:         ln,
		ReadBufferSize:   4096,
		BufferCount:      64,
		TxBytes:          1 << 20,
		MaxConnections:   1024,
		RingEntries:      256,
		RawEcho:          rawEcho,
		IdleTimeout:      time.Minute,
		HandshakeTimeout: time.Minute,
		CloseTimeout:     time.Second,
		ShutdownTimeout:  2 * time.Second,
		Logf:             t.Logf,
	})
	if err != nil {
		ln.Close()
		t.Fatalf("NewLoop: %v", err)
	}
	lp := l.(*loop)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = l.Shutdown()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("loop did not stop")
		}
	})
	return l, lp, cancel
}

func dial(t *testing.T, l Loop) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	return c
}

func TestRawEcho(t *testing.T) {
	l, lp, cancel := testLoopConfig(t, true)
	c := dial(t, l)
	defer c.Close()

	msg := []byte("hello ureactor")
	if _, err := c.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("echo = %q, want %q", got, msg)
	}

	cancel()
	_ = l.Shutdown()
	waitLive(t, l, 0)
	if !lp.rx.Balance() {
		t.Fatalf("rx arena imbalance: free=%d inuse=%d", lp.rx.FreeCount(), lp.rx.InUse())
	}
}

func TestRawEchoLarge(t *testing.T) {
	l, _, _ := testLoopConfig(t, true)
	c := dial(t, l)
	defer c.Close()

	msg := make([]byte, 16384)
	for i := range msg {
		msg[i] = byte(i)
	}
	go func() {
		_, _ = c.Write(msg)
	}()
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	for i := range msg {
		if got[i] != msg[i] {
			t.Fatalf("byte %d = %d, want %d", i, got[i], msg[i])
		}
	}
}

func TestManyConnections(t *testing.T) {
	l, lp, _ := testLoopConfig(t, true)

	const n = 200
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			msg := []byte{byte(i), byte(i >> 8), 'x'}
			if _, err := c.Write(msg); err != nil {
				errs <- err
				return
			}
			got := make([]byte, len(msg))
			if _, err := io.ReadFull(c, got); err != nil {
				errs <- err
				return
			}
			if string(got) != string(msg) {
				errs <- errors.New("echo mismatch")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Logf("stats=%+v rx{free=%d inuse=%d} parked=%d",
			l.Stats(), lp.rx.FreeCount(), lp.rx.InUse(), len(lp.parked))
		hw := lp.slab.HighWater()
		stuck := 0
		for slot := uint32(0); slot < hw && stuck < 20; slot++ {
			sl, ok := lp.slab.GetAny(slot)
			if !ok {
				continue
			}
			if sl.Flags == 0 && sl.InFlight == 0 {
				continue
			}
			t.Logf("slot=%d state=%s flags=%#x inflight=%d sendbid=%d", slot, sl.State, sl.Flags, sl.InFlight, sl.SendBid)
			stuck++
		}
		t.Fatalf("client: %v", err)
	}

	waitLive(t, l, 0)
	st := l.Stats()
	if st.Accepted < n {
		t.Fatalf("Accepted = %d, want >= %d", st.Accepted, n)
	}
	if st.StaleCompletions != 0 {
		t.Fatalf("stale completions = %d", st.StaleCompletions)
	}
	if !lp.rx.Balance() {
		t.Fatalf("rx arena imbalance: free=%d inuse=%d", lp.rx.FreeCount(), lp.rx.InUse())
	}
}

func TestBufferExhaustionParks(t *testing.T) {
	ln, err := netio.Listen("127.0.0.1:0", false, 128)
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLoop(LoopConfig{
		Listener:        ln,
		ReadBufferSize:  4096,
		BufferCount:     2,
		TxBytes:         1 << 20,
		MaxConnections:  64,
		RingEntries:     256,
		RawEcho:         true,
		IdleTimeout:     time.Minute,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	defer func() {
		cancel()
		_ = l.Shutdown()
		select {
		case <-done: //nolint:errcheck // wait for l.Run to return; the error is not asserted here
		case <-time.After(3 * time.Second):
			t.Error("loop did not stop")
		}
	}()

	const clients = 20
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			msg := []byte("park-me")
			if _, err := c.Write(msg); err != nil {
				errs <- err
				return
			}
			got := make([]byte, len(msg))
			if _, err := io.ReadFull(c, got); err != nil {
				errs <- err
				return
			}
			if string(got) != string(msg) {
				errs <- errors.New("mismatch")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("client: %v", err)
	}
}

func TestIdleTimeoutCloses(t *testing.T) {
	ln, err := netio.Listen("127.0.0.1:0", false, 128)
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLoop(LoopConfig{
		Listener:        ln,
		ReadBufferSize:  4096,
		BufferCount:     16,
		TxBytes:         1 << 20,
		MaxConnections:  16,
		RingEntries:     256,
		RawEcho:         true,
		IdleTimeout:     50 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	defer func() {
		cancel()
		_ = l.Shutdown()
		select {
		case <-done: //nolint:errcheck // wait for l.Run to return; the error is not asserted here
		case <-time.After(3 * time.Second):
			t.Error("loop did not stop")
		}
	}()

	c, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err == nil {
		t.Fatal("idle connection was not closed")
	}
	waitLive(t, l, 0)
	if l.Stats().TimerExpired == 0 {
		t.Fatal("timerExpired = 0")
	}
}

func TestShutdownWithOutstandingOps(t *testing.T) {
	l, _, _ := testLoopConfig(t, true)
	conns := make([]net.Conn, 32)
	for i := range conns {
		conns[i] = dial(t, l)
	}
	for _, c := range conns {
		_ = c.Close()
	}
	_ = l.Shutdown()
	waitLive(t, l, 0)
}

func waitLive(t *testing.T, l Loop, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l.Stats().Live == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Live = %d, want %d", l.Stats().Live, want)
}

func TestProbeReportsKernel(t *testing.T) {
	p := uring.Probe()
	if p.Kernel == "" {
		t.Fatal("probe did not report a kernel")
	}
	if !p.Ready() {
		t.Fatalf("probe not ready: %+v", p)
	}
}
