// Package ws implements an incremental RFC 6455 server-side frame parser.
//
// It consumes arbitrary completion boundaries: a header may be split byte by
// byte, one buffer may hold several frames, and a masked payload may span
// buffers. A message that fits in one buffer is delivered without copying or
// allocation; a fragmented or spanning message uses a spill buffer that is
// released as soon as the message completes.
//
// Application data is binary-only. A text frame is rejected at the header with
// 1003, before its payload is read.
package ws

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf8"
)

// Opcode is an RFC 6455 frame opcode.
type Opcode uint8

const (
	Continuation Opcode = 0x0
	Text         Opcode = 0x1
	Binary       Opcode = 0x2
	Close        Opcode = 0x8
	Ping         Opcode = 0x9
	Pong         Opcode = 0xA
)

// IsControl reports whether the opcode is a control frame.
func (o Opcode) IsControl() bool { return o&0x8 != 0 }

func (o Opcode) String() string {
	switch o {
	case Continuation:
		return "continuation"
	case Text:
		return "text"
	case Binary:
		return "binary"
	case Close:
		return "close"
	case Ping:
		return "ping"
	case Pong:
		return "pong"
	default:
		return "unknown"
	}
}

// Limits are the parser's resource bounds.
type Limits struct {
	MaxFrameSize   int64
	MaxMessageSize int64
	MaxFragments   int
}

// DefaultLimits matches Config's defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxFrameSize:   1 << 20,
		MaxMessageSize: 4 << 20,
		MaxFragments:   128,
	}
}

// ProtocolError is a WebSocket-level failure. Code is the close status the
// caller should send.
type ProtocolError struct {
	Code   uint16
	Reason string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("ws: %s (%d)", e.Reason, e.Code)
}

func proto(code uint16, reason string) *ProtocolError {
	return &ProtocolError{Code: code, Reason: reason}
}

// EventKind identifies a parsed event.
type EventKind uint8

const (
	EventMessage EventKind = iota
	EventPing
	EventPong
	EventClose
)

// Event is one parsed frame or message. Payload is borrowed and valid only for
// the callback.
type Event struct {
	Kind        EventKind
	Opcode      Opcode
	Payload     []byte
	CloseCode   uint16
	CloseReason string
}

// Handler receives parsed events. Returning an error stops parsing.
type Handler func(Event) error

type state uint8

const (
	stateHeader state = iota
	statePayload
)

// Parser is an incremental server-side frame parser. It is not safe for
// concurrent use.
type Parser struct {
	limits *Limits

	state     state
	header    [14]byte
	headerLen uint8

	fin        bool
	opcode     Opcode
	masked     bool
	mask       [4]byte
	maskOff    uint8
	payloadLen uint32
	remaining  uint32

	// Message assembly.
	fragmented bool
	msgOpcode  Opcode
	msgLen     uint32
	fragments  uint16
	frameSpill bool

	// Spill and control buffers exist only while a message spans completions
	// or a control frame is split across reads. An idle connection holds none,
	// and most messages never allocate this.
	extra *parserExtra
}

type parserExtra struct {
	spill []byte
	ctrl  []byte
}

func (p *Parser) ensureExtra() *parserExtra {
	if p.extra == nil {
		p.extra = &parserExtra{}
	}
	return p.extra
}

func (p *Parser) dropExtraIfIdle() {
	if p.extra != nil && p.extra.spill == nil && p.extra.ctrl == nil {
		p.extra = nil
	}
}

// NewParser returns a parser with the given limits. Zero values fall back to
// DefaultLimits.
func NewParser(limits Limits) *Parser {
	p := &Parser{}
	p.Init(limits)
	return p
}

// Init applies limits by value. It is used by tests and one-off parsers.
func (p *Parser) Init(limits Limits) {
	if limits.MaxFrameSize <= 0 {
		limits.MaxFrameSize = 1 << 20
	}
	if limits.MaxMessageSize <= 0 {
		limits.MaxMessageSize = 4 << 20
	}
	if limits.MaxFragments <= 0 {
		limits.MaxFragments = 128
	}
	p.InitLimits(&limits)
}

// InitLimits applies limits by pointer. The loop stores the three limits once
// per loop and every embedded parser points at them, so a connection does not
// carry its own copy.
func (p *Parser) InitLimits(limits *Limits) {
	if limits == nil {
		d := DefaultLimits()
		limits = &d
	}
	p.limits = limits
	p.Reset()
}

// Reset returns the parser to its initial state and releases any spill buffer.
func (p *Parser) Reset() {
	p.state = stateHeader
	p.headerLen = 0
	p.fin = false
	p.opcode = 0
	p.masked = false
	p.maskOff = 0
	p.payloadLen = 0
	p.remaining = 0
	p.fragmented = false
	p.msgOpcode = 0
	p.msgLen = 0
	p.fragments = 0
	p.extra = nil
	p.frameSpill = false
}

// Feed consumes as many complete frames as possible from data. The handler is
// called for each complete message, ping, pong, or close. data is not retained.
func (p *Parser) Feed(data []byte, fn Handler) error {
	for len(data) > 0 {
		if p.state == stateHeader {
			if err := p.readHeader(&data); err != nil {
				return err
			}
			if p.state == stateHeader {
				// Header incomplete.
				return nil
			}
			if p.remaining == 0 {
				// A zero-length continuation still completes or extends the
				// accumulated message; its payload is the spill, not nil.
				var payload []byte
				if !p.opcode.IsControl() && p.frameSpill && p.extra != nil {
					payload = p.extra.spill
				}
				if err := p.finishFrame(payload, fn); err != nil {
					return err
				}
				continue
			}
			continue
		}

		// Payload state.
		take := len(data)
		if take > int(p.remaining) {
			take = int(p.remaining)
		}
		chunk := data[:take]
		data = data[take:]
		p.remaining -= uint32(take)
		if p.masked {
			unmask(chunk, p.mask, int(p.maskOff))
			p.maskOff = (p.maskOff + uint8(take)) & 3
		}

		if p.opcode.IsControl() {
			var pending []byte
			if p.extra != nil {
				pending = p.extra.ctrl
			}
			if p.remaining > 0 || len(pending) > 0 {
				ex := p.ensureExtra()
				ex.ctrl = append(ex.ctrl, chunk...)
				if p.remaining == 0 {
					payload := ex.ctrl
					ex.ctrl = nil
					p.dropExtraIfIdle()
					if err := p.finishFrame(payload, fn); err != nil {
						return err
					}
				}
			} else {
				if err := p.finishFrame(chunk, fn); err != nil {
					return err
				}
			}
			continue
		}

		if p.frameSpill {
			ex := p.ensureExtra()
			if ex.spill == nil {
				ex.spill = make([]byte, 0, int(p.payloadLen))
			}
			ex.spill = append(ex.spill, chunk...)
			if p.remaining == 0 {
				if err := p.finishFrame(ex.spill, fn); err != nil {
					return err
				}
			}
			continue
		}

		if p.remaining > 0 {
			// This frame spans completions; start spilling from this chunk.
			p.frameSpill = true
			ex := p.ensureExtra()
			if ex.spill == nil {
				ex.spill = make([]byte, 0, int(p.payloadLen))
			}
			ex.spill = append(ex.spill, chunk...)
			continue
		}

		if err := p.finishFrame(chunk, fn); err != nil {
			return err
		}
	}
	return nil
}

func (p *Parser) readHeader(data *[]byte) error {
	for {
		need := 2
		if int(p.headerLen) >= 2 {
			if p.header[1]&0x80 == 0 {
				// An unmasked client frame is invalid. Parse the two-byte
				// header now so the error is reported instead of waiting for
				// a mask key that will never arrive.
				need = 2
			} else {
				code := p.header[1] & 0x7f
				switch code {
				case 126:
					need = 4
				case 127:
					need = 10
				default:
					need = 2
				}
				need += 4
			}
		}
		if int(p.headerLen) >= need {
			break
		}
		if len(*data) == 0 {
			return nil
		}
		n := copy(p.header[p.headerLen:need], *data)
		p.headerLen += uint8(n)
		*data = (*data)[n:]
	}
	if err := p.parseHeader(); err != nil {
		return err
	}
	p.state = statePayload
	return nil
}

func (p *Parser) parseHeader() error {
	b0, b1 := p.header[0], p.header[1]
	p.fin = b0&0x80 != 0
	if b0&0x70 != 0 {
		return proto(1002, "reserved bits set")
	}
	p.opcode = Opcode(b0 & 0x0f)
	p.masked = b1&0x80 != 0
	if !p.masked {
		return proto(1002, "client frame is not masked")
	}

	idx := 2
	switch b1 & 0x7f {
	case 126:
		p.payloadLen = uint32(binary.BigEndian.Uint16(p.header[2:4]))
		if p.payloadLen < 126 {
			return proto(1002, "non-minimal 16-bit length")
		}
		idx = 4
	case 127:
		if p.header[2]&0x80 != 0 {
			return proto(1002, "64-bit length has the high bit set")
		}
		v := binary.BigEndian.Uint64(p.header[2:10])
		if v < 65536 {
			return proto(1002, "non-minimal 64-bit length")
		}
		if v > math.MaxUint32 {
			return proto(1009, "frame too large")
		}
		p.payloadLen = uint32(v)
		idx = 10
	default:
		p.payloadLen = uint32(b1 & 0x7f)
	}
	copy(p.mask[:], p.header[idx:idx+4])

	if p.opcode.IsControl() {
		if !p.fin {
			return proto(1002, "fragmented control frame")
		}
		if p.payloadLen > 125 {
			return proto(1002, "control frame too large")
		}
		switch p.opcode {
		case Close, Ping, Pong:
		default:
			return proto(1002, "unknown control opcode")
		}
	} else {
		switch p.opcode {
		case Continuation:
			if !p.fragmented {
				return proto(1002, "continuation without a message")
			}
			if int(p.fragments)+1 > p.limits.MaxFragments {
				return proto(1009, "too many fragments")
			}
			p.fragments++
		case Binary:
			if p.fragmented {
				return proto(1002, "new data frame during a fragmented message")
			}
		case Text:
			return proto(1003, "text frames are not supported")
		default:
			return proto(1002, "unknown data opcode")
		}
	}

	if int64(p.payloadLen) > p.limits.MaxFrameSize {
		return proto(1009, "frame too large")
	}
	if !p.opcode.IsControl() {
		total := p.payloadLen
		if p.opcode == Continuation {
			total += p.msgLen
		}
		if int64(total) > p.limits.MaxMessageSize {
			return proto(1009, "message too large")
		}
	}

	p.maskOff = 0
	p.remaining = p.payloadLen
	p.frameSpill = !p.fin || p.fragmented
	if p.extra != nil {
		p.extra.ctrl = nil
		p.dropExtraIfIdle()
	}
	return nil
}

func (p *Parser) finishFrame(payload []byte, fn Handler) error {
	p.state = stateHeader
	p.headerLen = 0

	switch p.opcode {
	case Ping:
		return fn(Event{Kind: EventPing, Opcode: Ping, Payload: payload})
	case Pong:
		return fn(Event{Kind: EventPong, Opcode: Pong, Payload: payload})
	case Close:
		code, reason, err := parseClose(payload)
		if err != nil {
			return err
		}
		return fn(Event{Kind: EventClose, Opcode: Close, CloseCode: code, CloseReason: reason})
	}

	if p.fragmented {
		if !p.fin {
			p.msgLen += p.payloadLen
			return nil
		}
		var msg []byte
		if p.extra != nil {
			msg = p.extra.spill
		}
		op := p.msgOpcode
		p.resetMessage()
		return fn(Event{Kind: EventMessage, Opcode: op, Payload: msg})
	}

	if !p.fin {
		p.fragmented = true
		p.msgOpcode = p.opcode
		p.msgLen = p.payloadLen
		p.fragments = 0
		return nil
	}

	op := p.opcode
	p.resetMessage()
	return fn(Event{Kind: EventMessage, Opcode: op, Payload: payload})
}

func (p *Parser) resetMessage() {
	p.fragmented = false
	p.msgOpcode = 0
	p.msgLen = 0
	p.fragments = 0
	p.frameSpill = false
	if p.extra != nil {
		p.extra.spill = nil
		p.dropExtraIfIdle()
	}
}

func parseClose(payload []byte) (uint16, string, error) {
	if len(payload) == 1 {
		return 0, "", proto(1002, "close payload is one byte")
	}
	if len(payload) == 0 {
		return 1005, "", nil
	}
	code := binary.BigEndian.Uint16(payload[:2])
	if !validCloseCode(code) {
		return 0, "", proto(1002, "invalid close code")
	}
	reason := payload[2:]
	if !utf8.Valid(reason) {
		return 0, "", proto(1007, "close reason is not UTF-8")
	}
	return code, string(reason), nil
}

func validCloseCode(code uint16) bool {
	switch code {
	case 1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 1012, 1013, 1014:
		return true
	}
	return code >= 3000 && code <= 4999
}

func unmask(b []byte, mask [4]byte, off int) {
	for i := range b {
		b[i] ^= mask[(off+i)&3]
	}
}

// Mask applies the client mask to a payload. It is exported for tests and for
// the client-side helpers.
func Mask(b []byte, mask [4]byte, off int) {
	unmask(b, mask, off)
}
