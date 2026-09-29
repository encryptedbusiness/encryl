//go:build linux

package giouring

import "testing"

// TestPrepareCloseClearsOpcodeFlags guards the close path. OpcodeFlags is the
// rw_flags union. A reused SQE can still hold an accept, send, or cancel flag.
// The kernel rejects a close with a non-zero rw_flags with EINVAL, and the
// descriptor then stays open.
func TestPrepareCloseClearsOpcodeFlags(t *testing.T) {
	var e SubmissionQueueEntry
	e.OpcodeFlags = 0x1234
	e.PrepareClose(7)
	if e.OpcodeFlags != 0 {
		t.Fatalf("OpcodeFlags = %#x, want 0", e.OpcodeFlags)
	}
	if e.OpCode != OpClose {
		t.Fatalf("OpCode = %d, want OpClose (%d)", e.OpCode, OpClose)
	}
	if e.Fd != 7 {
		t.Fatalf("Fd = %d, want 7", e.Fd)
	}
}

func TestPrepareRWClearsStaleFields(t *testing.T) {
	var e SubmissionQueueEntry
	e.Flags = 0xff
	e.IoPrio = 1
	e.OpcodeFlags = 0xbeef
	e.Personality = 3
	e.SpliceFdIn = 9
	e.PrepareClose(11)
	if e.Flags != 0 || e.IoPrio != 0 || e.OpcodeFlags != 0 || e.Personality != 0 || e.SpliceFdIn != 0 {
		t.Fatalf("stale SQE fields: %+v", e)
	}
}
