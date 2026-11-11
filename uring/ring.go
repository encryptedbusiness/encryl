//go:build linux

package uring

import (
	"fmt"

	"github.com/encryptedbusiness/encryl/uring/giouring"
)

// NewRing creates, restricts, and enables a single-issuer ring. It must run on
// the OS thread that will submit; IORING_SETUP_SINGLE_ISSUER binds the ring to
// the creating task and returns EEXIST from another thread.
func NewRing(entries uint32) (*giouring.Ring, error) {
	if entries < 64 {
		entries = 64
	}
	ring := giouring.NewRing()
	// A larger CQ avoids overflow at high completion rates. Keep the CQ at
	// least twice the SQ size; the caller still owns the timer tick.
	cqEntries := entries * 2
	if cqEntries < 16384 {
		cqEntries = 16384
	}
	flags := uint32(giouring.SetupSingleIssuer | giouring.SetupRDisabled |
		giouring.SetupCoopTaskrun | giouring.SetupDeferTaskrun | giouring.SetupClamp)
	// R_DISABLED is mandatory for restrictions: an enabled ring rejects
	// IORING_REGISTER_RESTRICTIONS with EBADFD.
	if err := ring.QueueInitCQ(entries, cqEntries, flags); err != nil {
		return nil, fmt.Errorf("platform: ring setup: %w", err)
	}
	if _, err := ring.RegisterRestrictions(Restrictions()); err != nil {
		ring.QueueExit()
		return nil, fmt.Errorf("platform: register restrictions: %w", err)
	}
	if _, err := ring.EnableRings(); err != nil {
		ring.QueueExit()
		return nil, fmt.Errorf("platform: enable rings: %w", err)
	}
	return ring, nil
}

// Restrictions builds the opcode allowlist. io_uring request contents live in
// shared memory, so seccomp cannot inspect them; the kernel filters them here.
func Restrictions() []giouring.Restriction {
	allowedOps := []uint8{
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
	out := make([]giouring.Restriction, 0, len(allowedOps)+4)
	for _, op := range allowedOps {
		out = append(out, giouring.Restriction{
			OpCode:  uint16(giouring.RestrictionSQEOp),
			OpFlags: op,
		})
	}
	// Buffer selection and fixed-file flags are SQE flags, not opcodes.
	out = append(out, giouring.Restriction{
		OpCode:  uint16(giouring.RestrictionSQEFlagsAllowed),
		OpFlags: uint8(giouring.SqeBufferSelect),
	})
	out = append(out, giouring.Restriction{
		OpCode:  uint16(giouring.RestrictionSQEFlagsAllowed),
		OpFlags: uint8(giouring.SqeFixedFile),
	})
	// Restrictions gate register operations too.
	for _, reg := range []uint32{
		giouring.RegisterEventFD,
		giouring.UnregisterEventFD,
		giouring.RegisterFiles2,
		giouring.RegisterFilesUpdate,
		giouring.RegisterPbufRing,
		giouring.UnregisterPbufRing,
	} {
		out = append(out, giouring.Restriction{
			OpCode:  uint16(giouring.RestrictionRegisterOp),
			OpFlags: uint8(reg),
		})
	}
	return out
}
