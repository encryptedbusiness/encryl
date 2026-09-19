// Package event implements the encryl.v2 16-byte message header.
package event

import "encoding/binary"

// Version is the only envelope version this server accepts.
const Version uint8 = 2

// HeaderSize is the fixed envelope size in bytes.
const HeaderSize = 16

// Kind is the envelope kind.
type Kind uint8

const (
	KindAuth    Kind = 1
	KindEvent   Kind = 2
	KindAck     Kind = 3
	KindError   Kind = 4
	KindControl Kind = 5
)

// Flags are the envelope's optional bits. Reserved bits must be zero.
const (
	flagResponse uint16 = 1 << 0
	FlagNoReply  uint16 = 1 << 1
	flagMask     uint16 = flagResponse | FlagNoReply
)

// EventID rules from docs/protocol.md.
const (
	EventReservedZero = 0
	EventFirstApp     = 1024
)

// ErrorPayload codes.
const (
	ErrCodeUnauthenticated uint16 = 1
	ErrCodeUnauthorized    uint16 = 2
	ErrCodeUnknownEvent    uint16 = 3
	ErrCodePayloadTooLarge uint16 = 4
	ErrCodeRateLimited     uint16 = 5
	ErrCodeBackpressure    uint16 = 6
	ErrCodeInternal        uint16 = 7
)

// Envelope is one decoded v2 header plus its borrowed payload.
type Envelope struct {
	Kind      Kind
	Flags     uint16
	Event     uint32
	RequestID uint64
	Payload   []byte
}

// DecodeError describes a malformed event. The reactor closes the
// connection with 1002 for every decode error.
type DecodeError struct {
	Reason string
}

func (e *DecodeError) Error() string { return "event: " + e.Reason }

var (
	ErrShort   = &DecodeError{Reason: "message shorter than 16 bytes"}
	ErrVersion = &DecodeError{Reason: "unsupported envelope version"}
	ErrFlags   = &DecodeError{Reason: "reserved flag bits set"}
	ErrKind    = &DecodeError{Reason: "unknown envelope kind"}
	ErrEvent   = &DecodeError{Reason: "EVENT with event ID 0"}
)

// Decode parses a complete binary WebSocket message.
func Decode(b []byte) (Envelope, error) {
	if len(b) < HeaderSize {
		return Envelope{}, ErrShort
	}
	if b[0] != Version {
		return Envelope{}, ErrVersion
	}
	kind := Kind(b[1])
	if kind < KindAuth || kind > KindControl {
		return Envelope{}, ErrKind
	}
	flags := binary.BigEndian.Uint16(b[2:4])
	if flags&^flagMask != 0 {
		return Envelope{}, ErrFlags
	}
	event := binary.BigEndian.Uint32(b[4:8])
	if kind == KindEvent && event == EventReservedZero {
		return Envelope{}, ErrEvent
	}
	return Envelope{
		Kind:      kind,
		Flags:     flags,
		Event:     event,
		RequestID: binary.BigEndian.Uint64(b[8:16]),
		Payload:   b[HeaderSize:],
	}, nil
}

// Encode writes an envelope header into dst and returns the header size. dst
// must have room for at least HeaderSize bytes.
func Encode(dst []byte, e Envelope) int {
	dst[0] = Version
	dst[1] = byte(e.Kind)
	binary.BigEndian.PutUint16(dst[2:4], e.Flags)
	binary.BigEndian.PutUint32(dst[4:8], e.Event)
	binary.BigEndian.PutUint64(dst[8:16], e.RequestID)
	return HeaderSize
}

// EncodeMessage allocates a message containing the header and payload. It is
// used by the public Send path, where the caller owns the result until the
// owning loop has copied it into an outbound buffer.
func EncodeMessage(e Envelope, payload []byte) []byte {
	buf := make([]byte, HeaderSize+len(payload))
	Encode(buf, e)
	copy(buf[HeaderSize:], payload)
	return buf
}
