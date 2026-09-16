package ws

import "encoding/binary"

// FrameHeaderLen returns the number of header bytes required for a payload of
// n bytes.
func FrameHeaderLen(n int) int {
	switch {
	case n < 126:
		return 2
	case n <= 65535:
		return 4
	default:
		return 10
	}
}

// AppendFrameHeader writes a server-side (unmasked, final) frame header for a
// payload of n bytes into dst and returns the header length. dst must have
// room for FrameHeaderLen(n) bytes.
func AppendFrameHeader(dst []byte, op Opcode, n int) int {
	dst[0] = 0x80 | byte(op)
	switch {
	case n < 126:
		dst[1] = byte(n)
		return 2
	case n <= 65535:
		dst[1] = 126
		binary.BigEndian.PutUint16(dst[2:4], uint16(n))
		return 4
	default:
		dst[1] = 127
		binary.BigEndian.PutUint64(dst[2:10], uint64(n))
		return 10
	}
}
