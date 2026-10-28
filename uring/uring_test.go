//go:build linux

package uring

import (
	"testing"

	"github.com/encryptedbusiness/encryl/uring/giouring"
)

func TestParseKernel(t *testing.T) {
	tests := []struct {
		release string
		major   int
		minor   int
		ok      bool
	}{
		{"6.8.0-147-generic", 6, 8, true},
		{"6.6", 6, 6, true},
		{"5.15.0-1-arm64", 5, 15, true},
		{"7.0.0", 7, 0, true},
		{"linux", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tt := range tests {
		major, minor, err := parseKernel(tt.release)
		if tt.ok && err != nil {
			t.Errorf("parseKernel(%q) error: %v", tt.release, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("parseKernel(%q) accepted an invalid release", tt.release)
		}
		if tt.ok && (major != tt.major || minor != tt.minor) {
			t.Errorf("parseKernel(%q) = %d.%d, want %d.%d", tt.release, major, minor, tt.major, tt.minor)
		}
	}
}

func TestRestrictionsCoverRequiredOpcodes(t *testing.T) {
	wantOps := []uint8{
		giouring.OpAccept,
		giouring.OpPollAdd,
		giouring.OpPollRemove,
		giouring.OpRecv,
		giouring.OpSend,
		giouring.OpClose,
		giouring.OpAsyncCancel,
		giouring.OpProvideBuffers,
		giouring.OpRead,
	}
	rules := Restrictions()
	if len(rules) < len(wantOps) {
		t.Fatalf("restrictions = %d, want at least %d", len(rules), len(wantOps))
	}

	ops := map[uint8]bool{}
	var sqeFlags, registerOps int
	for _, r := range rules {
		switch r.OpCode {
		case uint16(giouring.RestrictionSQEOp):
			ops[r.OpFlags] = true
		case uint16(giouring.RestrictionSQEFlagsAllowed):
			sqeFlags++
		case uint16(giouring.RestrictionRegisterOp):
			registerOps++
		}
	}
	for _, op := range wantOps {
		if !ops[op] {
			t.Errorf("opcode %d is not allowed", op)
		}
	}
	if sqeFlags < 2 {
		t.Errorf("SQE flag restrictions = %d, want >= 2", sqeFlags)
	}
	if registerOps < 2 {
		t.Errorf("register restrictions = %d, want >= 2", registerOps)
	}
}

func TestRXArenaTakePutAndBalance(t *testing.T) {
	a, err := NewRXArena(64, 4)
	if err != nil {
		t.Fatalf("NewRXArena: %v", err)
	}
	defer a.Close()

	if a.Size() != 64 || a.FreeCount() != 4 || a.InUse() != 0 {
		t.Fatalf("arena = size %d free %d in-use %d", a.Size(), a.FreeCount(), a.InUse())
	}
	if !a.Balance() {
		t.Fatal("arena not balanced at rest")
	}

	seen := map[int32]bool{}
	for i := 0; i < 4; i++ {
		bid, ok := a.Take()
		if !ok {
			t.Fatalf("Take %d failed", i)
		}
		if seen[bid] {
			t.Fatalf("buffer %d handed out twice", bid)
		}
		seen[bid] = true
		if got := a.Bytes(bid); len(got) != 64 {
			t.Fatalf("Bytes(%d) length = %d", bid, len(got))
		}
	}
	if _, ok := a.Take(); ok {
		t.Fatal("Take succeeded with no buffers left")
	}
	if !a.Balance() {
		t.Fatal("arena not balanced when exhausted")
	}
	if a.InUse() != 4 {
		t.Fatalf("InUse = %d, want 4", a.InUse())
	}
	if a.Addr(0) == a.Addr(1) {
		t.Fatal("buffer addresses are not distinct")
	}

	if !a.Put(0) {
		t.Fatal("Put of a reserved buffer failed")
	}
	if a.Put(0) {
		t.Fatal("double Put succeeded")
	}
	if a.Put(-1) || a.Put(99) {
		t.Fatal("Put of an out-of-range buffer succeeded")
	}
	if a.FreeCount() != 1 || a.InUse() != 3 {
		t.Fatalf("after Put: free %d in-use %d", a.FreeCount(), a.InUse())
	}
	if !a.Balance() {
		t.Fatal("arena not balanced after Put")
	}
}

func TestRXArenaRejectsInvalidSizes(t *testing.T) {
	if _, err := NewRXArena(0, 1); err == nil {
		t.Fatal("zero buffer size accepted")
	}
	if _, err := NewRXArena(1, 0); err == nil {
		t.Fatal("zero buffer count accepted")
	}
	if _, err := NewRXArena(-1, 1); err == nil {
		t.Fatal("negative buffer size accepted")
	}
}

func TestTXArenaOwnership(t *testing.T) {
	a, err := NewTXArena(256, 64)
	if err != nil {
		t.Fatalf("NewTXArena: %v", err)
	}
	defer a.Close()

	if a.Size() != 64 || a.InUse() != 0 {
		t.Fatalf("arena = size %d in-use %d", a.Size(), a.InUse())
	}
	if _, ok := a.Take(1, 1, 65); ok {
		t.Fatal("a block larger than the arena was reserved")
	}

	bids := make([]int32, 0, 4)
	for i := 0; i < 4; i++ {
		bid, ok := a.Take(uint32(i), 1, 32)
		if !ok {
			t.Fatalf("Take %d failed", i)
		}
		bids = append(bids, bid)
	}
	if _, ok := a.Take(9, 1, 1); ok {
		t.Fatal("Take succeeded with no blocks left")
	}
	if a.InUse() != 4 {
		t.Fatalf("InUse = %d, want 4", a.InUse())
	}

	// A release from the wrong owner must not free the block.
	if a.Release(bids[0], 99, 1) || a.Release(bids[0], 0, 99) {
		t.Fatal("release by the wrong owner succeeded")
	}
	if a.InUse() != 4 {
		t.Fatal("failed release changed the accounting")
	}
	if !a.Release(bids[0], 0, 1) {
		t.Fatal("release by the right owner failed")
	}
	if a.InUse() != 3 {
		t.Fatalf("InUse = %d, want 3", a.InUse())
	}
	if a.Release(-1, 0, 1) || a.Release(99, 0, 1) {
		t.Fatal("release of an out-of-range block succeeded")
	}
}

func TestTXArenaRejectsInvalidSizes(t *testing.T) {
	if _, err := NewTXArena(0, 1); err == nil {
		t.Fatal("zero bytes accepted")
	}
	if _, err := NewTXArena(1, 0); err == nil {
		t.Fatal("zero block size accepted")
	}
}

func TestArenasCloseIdempotently(t *testing.T) {
	rx, err := NewRXArena(16, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := rx.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := rx.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	tx, err := NewTXArena(64, 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := tx.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestProbeReportsConsistentState(t *testing.T) {
	report := Probe()
	if report.Kernel == "" {
		t.Fatal("Probe did not read the kernel release")
	}
	if report.Ready() != (len(report.Problems) == 0) {
		t.Fatalf("Ready = %v with %d problems", report.Ready(), len(report.Problems))
	}
	if MinimumKernel.Major < 6 {
		t.Fatalf("MinimumKernel = %d.%d", MinimumKernel.Major, MinimumKernel.Minor)
	}
}

func TestNewRingOrSkip(t *testing.T) {
	ring, err := NewRing(64)
	if err != nil {
		t.Skipf("io_uring is unavailable on this host: %v", err)
	}
	defer ring.QueueExit()
}
