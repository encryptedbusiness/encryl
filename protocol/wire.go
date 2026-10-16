package protocol

import "encoding/binary"

const (
	wireVarint = 0
	wireI64    = 1
	wireBytes  = 2
	wireI32    = 5
)

func appendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

func varintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

func appendKey(dst []byte, field, wire int) []byte {
	return appendVarint(dst, uint64(field)<<3|uint64(wire))
}

func keyLen(field, wire int) int {
	return varintLen(uint64(field)<<3 | uint64(wire))
}

// appendUint writes a varint field. The zero value is omitted, matching
// proto3 default-value semantics.
func appendUint(dst []byte, field int, v uint64) []byte {
	if v == 0 {
		return dst
	}
	dst = appendKey(dst, field, wireVarint)
	return appendVarint(dst, v)
}

func uintSize(field int, v uint64) int {
	if v == 0 {
		return 0
	}
	return keyLen(field, wireVarint) + varintLen(v)
}

// appendBytes writes a bytes/string field. An empty value is omitted, matching
// proto3.
func appendBytes(dst []byte, field int, b []byte) []byte {
	if len(b) == 0 {
		return dst
	}
	dst = appendKey(dst, field, wireBytes)
	dst = appendVarint(dst, uint64(len(b)))
	return append(dst, b...)
}

func bytesSize(field int, b []byte) int {
	if len(b) == 0 {
		return 0
	}
	return keyLen(field, wireBytes) + varintLen(uint64(len(b))) + len(b)
}

func framedSize(msgSize int) int {
	return varintLen(uint64(msgSize)) + msgSize
}

func readVarint(b []byte, i int) (uint64, int, bool) {
	v, n := binary.Uvarint(b[i:])
	if n <= 0 {
		return 0, i, false
	}
	return v, i + n, true
}

func readKey(b []byte, i int) (field int, wire int, next int, ok bool) {
	v, n, ok := readVarint(b, i)
	if !ok {
		return 0, 0, i, false
	}
	field = int(v >> 3)
	wire = int(v & 7)
	if field <= 0 {
		return 0, 0, i, false
	}
	return field, wire, n, true
}

func readBytes(b []byte, i int) ([]byte, int, bool) {
	l, n, ok := readVarint(b, i)
	if !ok {
		return nil, i, false
	}
	from := n
	if l > uint64(len(b)-from) {
		return nil, i, false
	}
	to := from + int(l)
	return b[from:to], to, true
}

func skipField(b []byte, i, wire int) (int, bool) {
	switch wire {
	case wireVarint:
		_, n, ok := readVarint(b, i)
		return n, ok
	case wireI64:
		if i+8 > len(b) {
			return i, false
		}
		return i + 8, true
	case wireBytes:
		_, n, ok := readBytes(b, i)
		return n, ok
	case wireI32:
		if i+4 > len(b) {
			return i, false
		}
		return i + 4, true
	default:
		return i, false
	}
}
