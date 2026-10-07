// Package platform is the Linux kernel boundary. It owns the io_uring ring,
// completion tags, listener, and the fixed per-loop buffer arenas. It does not
// know about connections, events, rooms, or the public API.
package reactor

// Op identifies what a completion belongs to. It is packed into the top byte
// of the io_uring user_data field.
type Op uint8

const (
	OpInvalid Op = iota
	OpAccept
	OpPoll
	OpRecv
	OpSend
	OpClose
	OpCancel
	OpProvide
	OpRead
	OpMsgRing
	OpEventFD
)

const (
	opShift  = 56
	genShift = 24
	slotMask = uint64(1)<<24 - 1
	genMask  = uint64(1)<<32 - 1
	opMax    = uint8(OpEventFD)
)

// EncodeUserData packs an operation, local slot, and generation.
//
// Layout: [op:8][generation:32][slot:24]. The kernel treats user_data as an
// opaque u64 and never dereferences it, so no Go pointer may ever be placed
// here. Only integers cross the boundary.
func EncodeUserData(op Op, slot, gen uint32) uint64 {
	return uint64(op)<<opShift |
		(uint64(gen)&genMask)<<genShift |
		uint64(slot)&slotMask
}

// DecodeUserData unpacks a completion tag. It rejects unknown operations and
// out-of-range slots rather than letting a malformed completion index the
// slab.
func DecodeUserData(u uint64) (op Op, slot, gen uint32, ok bool) {
	op = Op(u >> opShift)
	if op == OpInvalid || uint8(op) > opMax {
		return OpInvalid, 0, 0, false
	}
	gen = uint32((u >> genShift) & genMask)
	slot = uint32(u & slotMask)
	return op, slot, gen, true
}
