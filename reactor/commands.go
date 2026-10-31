//go:build linux

package reactor

import (
	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/event"
)

func (l *loop) drainCommands() {
	for {
		c, ok := l.cmd.Pop()
		if !ok {
			return
		}
		switch c.Kind {
		case CmdClose:
			code := uint16(c.A)
			if code == 0 {
				code = 1001
			}
			l.sendCloseData(c.Slot, c.Gen, code, c.Data)
		case CmdSend:
			l.enqueueOwned(c.Slot, c.Gen, c.Frame)
		case CmdFanout:
			l.c.fanoutCmds.Add(1)
			if c.Frame == nil {
				c.Batch.Release()
				continue
			}
			if c.Batch != nil {
				for _, ref := range c.Batch.Refs {
					if int(ref.Loop) != l.cfg.Index {
						continue
					}
					l.c.fanoutRefs.Add(1)
					c.Frame.Retain()
					l.enqueueOwned(ref.Slot, ref.Gen, c.Frame)
				}
				c.Batch.Release()
			}
			c.Frame.Release()
		case CmdBroadcast:
			if c.Frame == nil {
				continue
			}
			hw := l.slab.HighWater()
			for slot := uint32(0); slot < hw; slot++ {
				sl, ok := l.slab.GetAny(slot)
				if !ok || sl.State != connection.StateOpen {
					continue
				}
				if sl.Encoding != c.Encoding {
					continue
				}
				if c.HasSelfExcept {
					self := connection.Ref{Loop: uint16(l.cfg.Index), Slot: slot, Gen: sl.Gen}
					if self == c.SelfExcept {
						continue
					}
				}
				if excludedRef(c.Except, uint16(l.cfg.Index), slot, sl.Gen) {
					continue
				}
				c.Frame.Retain()
				l.enqueueOwned(slot, sl.Gen, c.Frame)
			}
			c.Frame.Release()
		case CmdAckArm:
			// A packs the slab generation in the high half so a stale timer
			// cannot consume a rebound entry.
			ackSlot, ackGen := event.UnpackAckToken(c.A)
			if !l.armAckTimer(ackSlot, ackGen, c.B) {
				l.c.ackTimeouts.Add(1)
				if l.cfg.OnAckTimeout != nil {
					l.cfg.OnAckTimeout(ackSlot, ackGen)
				}
			}
		case CmdAckCancel:
			l.cancelAckTimer(uint32(c.A))
		}
	}
}

// excludedRef matches the full Ref. The loop is part of the identity: a
// slot-and-generation pair repeats on every loop, so omitting the loop would
// exclude one wrong connection per loop from every broadcast.
func excludedRef(refs []connection.Ref, loop uint16, slot, gen uint32) bool {
	for _, ref := range refs {
		if ref.Loop == loop && ref.Slot == slot && ref.Gen == gen {
			return true
		}
	}
	return false
}
