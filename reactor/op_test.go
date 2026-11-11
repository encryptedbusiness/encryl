package reactor

import "testing"

func TestUserDataRoundTrip(t *testing.T) {
	ops := []Op{OpAccept, OpPoll, OpRecv, OpSend, OpClose, OpCancel, OpProvide, OpRead, OpMsgRing, OpEventFD}
	for _, op := range ops {
		for _, slot := range []uint32{0, 1, 1023, 1<<24 - 1} {
			for _, gen := range []uint32{0, 1, 42, ^uint32(0)} {
				u := EncodeUserData(op, slot, gen)
				gotOp, gotSlot, gotGen, ok := DecodeUserData(u)
				if !ok {
					t.Fatalf("DecodeUserData(%#x) rejected", u)
				}
				if gotOp != op || gotSlot != slot || gotGen != gen {
					t.Fatalf("round trip = (%v,%d,%d), want (%v,%d,%d)", gotOp, gotSlot, gotGen, op, slot, gen)
				}
			}
		}
	}
}

func TestDecodeUserDataRejectsUnknownOp(t *testing.T) {
	u := EncodeUserData(Op(200), 1, 1)
	if _, _, _, ok := DecodeUserData(u); ok {
		t.Fatal("unknown op accepted")
	}
	if _, _, _, ok := DecodeUserData(0); ok {
		t.Fatal("zero op accepted")
	}
}

func FuzzUserData(f *testing.F) {
	f.Add(uint8(OpRecv), uint32(1), uint32(1))
	f.Add(uint8(OpPoll), uint32(0), uint32(0))
	f.Add(uint8(255), uint32(1<<24-1), ^uint32(0))

	f.Fuzz(func(t *testing.T, op uint8, slot, gen uint32) {
		u := EncodeUserData(Op(op), slot, gen)
		gotOp, gotSlot, gotGen, ok := DecodeUserData(u)
		if !ok {
			return
		}
		if gotOp != Op(op) || gotSlot != slot&uint32(slotMask) || gotGen != gen {
			t.Fatalf("round trip mismatch for op=%d slot=%d gen=%d", op, slot, gen)
		}
	})
}
