// Bebop is a hand-written, zero-allocation codec for the encryl event
// protocol.
//
// The value encodings follow the Bebop wire format: fixed-width little-endian
// integers, 32-bit lengths for strings/bytes/arrays, and 1-byte enums/bools.
//
// The record layout is tuned for the encryl hot paths rather than for generated
// Bebop structs: every record has a one-byte presence bitmap, so zero-valued
// fields cost nothing, and pushes use a one-byte frame type instead of a full
// reply header. That keeps publish and event-push frames compact while
// retaining Bebop's fixed-width decode. The exact layout is documented in
// protocol/bebop.bop.
//
// Frames are varint-length-prefixed.
package protocol

import "encoding/binary"

// Presence bits. The meaning is per record.
const (
	bbBit0 byte = 1 << 0
	bbBit1 byte = 1 << 1
	bbBit2 byte = 1 << 2
	bbBit3 byte = 1 << 3
	bbBit4 byte = 1 << 4
	bbBit5 byte = 1 << 5
	bbBit6 byte = 1 << 6
)

// Frame types inside one varint-delimited message.
const (
	bbFrameReply byte = 0
	bbFramePush  byte = 2
)

func bbAppendUvarint(dst []byte, v uint64) []byte { return binary.AppendUvarint(dst, v) }

func bbAppendU32(dst []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(dst, v) }

func bbAppendU64(dst []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(dst, v) }

func bbAppendBytes(dst, b []byte) []byte {
	dst = bbAppendU32(dst, uint32(len(b)))
	return append(dst, b...)
}

func bbBytesSize(b []byte) int { return 4 + len(b) }

func bbUvarintSize(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

func bbReadU8(b []byte, i int) (byte, int, bool) {
	if i >= len(b) {
		return 0, i, false
	}
	return b[i], i + 1, true
}

func bbReadU32(b []byte, i int) (uint32, int, bool) {
	if i+4 > len(b) {
		return 0, i, false
	}
	return binary.LittleEndian.Uint32(b[i:]), i + 4, true
}

func bbReadU64(b []byte, i int) (uint64, int, bool) {
	if i+8 > len(b) {
		return 0, i, false
	}
	return binary.LittleEndian.Uint64(b[i:]), i + 8, true
}

func bbReadBytes(b []byte, i int) ([]byte, int, bool) {
	l, i, ok := bbReadU32(b, i)
	if !ok || uint64(l) > uint64(len(b)-i) {
		return nil, i, false
	}
	return b[i : i+int(l)], i + int(l), true
}

func bbPublishFlags(p *Publish) byte {
	var f byte
	if p.RoomID != 0 {
		f |= bbBit0
	}
	if len(p.Room) != 0 {
		f |= bbBit1
	}
	if p.Event.ID != 0 {
		f |= bbBit2
	}
	if len(p.Event.Name) != 0 {
		f |= bbBit3
	}
	if len(p.Payload) != 0 {
		f |= bbBit4
	}
	if p.RequestID != 0 {
		f |= bbBit5
	}
	return f
}

func bbSendFlags(s *Send) byte {
	var f byte
	if s.Event.ID != 0 {
		f |= bbBit0
	}
	if len(s.Event.Name) != 0 {
		f |= bbBit1
	}
	if len(s.Payload) != 0 {
		f |= bbBit2
	}
	if s.RequestID != 0 {
		f |= bbBit3
	}
	return f
}

func bbEventPushFlags(e *EventPush) byte {
	var f byte
	if e.RoomID != 0 {
		f |= bbBit0
	}
	if len(e.Room) != 0 {
		f |= bbBit1
	}
	if e.EventID != 0 {
		f |= bbBit2
	}
	if len(e.Payload) != 0 {
		f |= bbBit3
	}
	if e.RequestID != 0 {
		f |= bbBit4
	}
	if e.Offset != 0 {
		f |= bbBit5
	}
	return f
}

func bbMessagePushFlags(m *Message) byte {
	var f byte
	if m.EventID != 0 {
		f |= bbBit0
	}
	if len(m.Payload) != 0 {
		f |= bbBit1
	}
	if m.RequestID != 0 {
		f |= bbBit2
	}
	return f
}

// AppendFramedCommandBebop appends one varint-length-prefixed Bebop Command.
func AppendFramedCommandBebop(dst []byte, c *Command) []byte {
	dst = bbAppendUvarint(dst, uint64(bbCommandSize(c)))
	return bbAppendCommand(dst, c)
}

// FramedCommandSizeBebop reports the encoded size of AppendFramedCommandBebop.
func FramedCommandSizeBebop(c *Command) int {
	n := bbCommandSize(c)
	return bbUvarintSize(uint64(n)) + n
}

// AppendFramedReplyBebop appends one varint-length-prefixed Bebop Reply.
func AppendFramedReplyBebop(dst []byte, r *Reply) []byte {
	dst = bbAppendUvarint(dst, uint64(bbReplySize(r)))
	return bbAppendReply(dst, r)
}

// FramedReplySizeBebop reports the encoded size of AppendFramedReplyBebop.
func FramedReplySizeBebop(r *Reply) int {
	n := bbReplySize(r)
	return bbUvarintSize(uint64(n)) + n
}

// AppendFramedPushBebop appends one varint-length-prefixed Bebop push.
func AppendFramedPushBebop(dst []byte, p *Push) []byte {
	r := Reply{Kind: ReplyPush, Push: *p}
	return AppendFramedReplyBebop(dst, &r)
}

// FramedPushSizeBebop reports the encoded size of AppendFramedPushBebop.
func FramedPushSizeBebop(p *Push) int {
	r := Reply{Kind: ReplyPush, Push: *p}
	return FramedReplySizeBebop(&r)
}

func bbCommandSize(c *Command) int {
	n := 4 + 1
	switch c.Kind {
	case CommandConnect:
		var f byte
		if len(c.Connect.Token) != 0 {
			f |= bbBit0
		}
		if len(c.Connect.Client) != 0 {
			f |= bbBit1
		}
		if len(c.Connect.Version) != 0 {
			f |= bbBit2
		}
		if len(c.Connect.Subscriptions) != 0 {
			f |= bbBit3
		}
		n++
		if f&bbBit0 != 0 {
			n += bbBytesSize(c.Connect.Token)
		}
		if f&bbBit1 != 0 {
			n += bbBytesSize(c.Connect.Client)
		}
		if f&bbBit2 != 0 {
			n += bbBytesSize(c.Connect.Version)
		}
		if f&bbBit3 != 0 {
			n += 4
			for _, s := range c.Connect.Subscriptions {
				n += bbBytesSize(s)
			}
		}
	case CommandSubscribe:
		n += bbBytesSize(c.Subscribe.Room)
	case CommandUnsubscribe:
		n += 4
	case CommandPublish:
		p := &c.Publish
		f := bbPublishFlags(p)
		n++
		if f&bbBit0 != 0 {
			n += 4
		}
		if f&bbBit1 != 0 {
			n += bbBytesSize(p.Room)
		}
		if f&bbBit2 != 0 {
			n += 4
		}
		if f&bbBit3 != 0 {
			n += bbBytesSize(p.Event.Name)
		}
		if f&bbBit4 != 0 {
			n += bbBytesSize(p.Payload)
		}
		if f&bbBit5 != 0 {
			n += 8
		}
	case CommandSend:
		s := &c.Send
		f := bbSendFlags(s)
		n++
		if f&bbBit0 != 0 {
			n += 4
		}
		if f&bbBit1 != 0 {
			n += bbBytesSize(s.Event.Name)
		}
		if f&bbBit2 != 0 {
			n += bbBytesSize(s.Payload)
		}
		if f&bbBit3 != 0 {
			n += 8
		}
	case CommandAck:
		n += 8 + 1
		if len(c.Ack.Payload) != 0 {
			n += bbBytesSize(c.Ack.Payload)
		}
	}
	return n
}

func bbAppendCommand(dst []byte, c *Command) []byte {
	dst = bbAppendU32(dst, c.ID)
	dst = append(dst, byte(c.Kind))
	switch c.Kind {
	case CommandConnect:
		var f byte
		if len(c.Connect.Token) != 0 {
			f |= bbBit0
		}
		if len(c.Connect.Client) != 0 {
			f |= bbBit1
		}
		if len(c.Connect.Version) != 0 {
			f |= bbBit2
		}
		if len(c.Connect.Subscriptions) != 0 {
			f |= bbBit3
		}
		dst = append(dst, f)
		if f&bbBit0 != 0 {
			dst = bbAppendBytes(dst, c.Connect.Token)
		}
		if f&bbBit1 != 0 {
			dst = bbAppendBytes(dst, c.Connect.Client)
		}
		if f&bbBit2 != 0 {
			dst = bbAppendBytes(dst, c.Connect.Version)
		}
		if f&bbBit3 != 0 {
			dst = bbAppendU32(dst, uint32(len(c.Connect.Subscriptions)))
			for _, s := range c.Connect.Subscriptions {
				dst = bbAppendBytes(dst, s)
			}
		}
	case CommandSubscribe:
		dst = bbAppendBytes(dst, c.Subscribe.Room)
	case CommandUnsubscribe:
		dst = bbAppendU32(dst, c.Unsubscribe.RoomID)
	case CommandPublish:
		p := &c.Publish
		f := bbPublishFlags(p)
		dst = append(dst, f)
		if f&bbBit0 != 0 {
			dst = bbAppendU32(dst, p.RoomID)
		}
		if f&bbBit1 != 0 {
			dst = bbAppendBytes(dst, p.Room)
		}
		if f&bbBit2 != 0 {
			dst = bbAppendU32(dst, p.Event.ID)
		}
		if f&bbBit3 != 0 {
			dst = bbAppendBytes(dst, p.Event.Name)
		}
		if f&bbBit4 != 0 {
			dst = bbAppendBytes(dst, p.Payload)
		}
		if f&bbBit5 != 0 {
			dst = bbAppendU64(dst, p.RequestID)
		}
	case CommandSend:
		s := &c.Send
		f := bbSendFlags(s)
		dst = append(dst, f)
		if f&bbBit0 != 0 {
			dst = bbAppendU32(dst, s.Event.ID)
		}
		if f&bbBit1 != 0 {
			dst = bbAppendBytes(dst, s.Event.Name)
		}
		if f&bbBit2 != 0 {
			dst = bbAppendBytes(dst, s.Payload)
		}
		if f&bbBit3 != 0 {
			dst = bbAppendU64(dst, s.RequestID)
		}
	case CommandAck:
		dst = bbAppendU64(dst, c.Ack.Token)
		if len(c.Ack.Payload) != 0 {
			dst = append(dst, 1)
			dst = bbAppendBytes(dst, c.Ack.Payload)
		} else {
			dst = append(dst, 0)
		}
	}
	return dst
}

func bbReplySize(r *Reply) int {
	if r.Kind == ReplyPush {
		return bbPushFrameSize(&r.Push)
	}
	n := 1 + 4 + 1 + 1
	if r.Error != nil {
		n += 4 + bbBytesSize(r.Error.Message)
	}
	switch r.Kind {
	case ReplyConnect:
		var f byte
		if len(r.Connect.ClientID) != 0 {
			f |= bbBit0
		}
		if r.Connect.Pong {
			f |= bbBit1
		}
		if len(r.Connect.Subscriptions) != 0 {
			f |= bbBit2
		}
		n += 1 + 4
		if f&bbBit0 != 0 {
			n += bbBytesSize(r.Connect.ClientID)
		}
		if f&bbBit2 != 0 {
			n += 4 + 8*len(r.Connect.Subscriptions)
		}
	case ReplySubscribe:
		n += 8
	case ReplyPublish:
	case ReplyAck:
		if len(r.Ack.Payload) != 0 {
			n += 1 + bbBytesSize(r.Ack.Payload)
		} else {
			n++
		}
	}
	return n
}

func bbAppendReply(dst []byte, r *Reply) []byte {
	if r.Kind == ReplyPush {
		dst = append(dst, bbFramePush)
		return bbAppendPush(dst, &r.Push)
	}
	dst = append(dst, bbFrameReply)
	dst = bbAppendU32(dst, r.ID)
	dst = append(dst, byte(r.Kind))
	var flags byte
	if r.Error != nil {
		flags |= bbBit0
		if r.Error.Temporary {
			flags |= bbBit1
		}
	}
	dst = append(dst, flags)
	if r.Error != nil {
		dst = bbAppendU32(dst, r.Error.Code)
		dst = bbAppendBytes(dst, r.Error.Message)
	}
	switch r.Kind {
	case ReplyConnect:
		var f byte
		if len(r.Connect.ClientID) != 0 {
			f |= bbBit0
		}
		if r.Connect.Pong {
			f |= bbBit1
		}
		if len(r.Connect.Subscriptions) != 0 {
			f |= bbBit2
		}
		dst = append(dst, f)
		dst = bbAppendU32(dst, r.Connect.PingSeconds)
		if f&bbBit0 != 0 {
			dst = bbAppendBytes(dst, r.Connect.ClientID)
		}
		if f&bbBit2 != 0 {
			dst = bbAppendU32(dst, uint32(len(r.Connect.Subscriptions)))
			for _, s := range r.Connect.Subscriptions {
				dst = bbAppendU32(dst, s.RoomID)
				dst = bbAppendU32(dst, s.EventID)
			}
		}
	case ReplySubscribe:
		dst = bbAppendU32(dst, r.Subscribe.RoomID)
		dst = bbAppendU32(dst, r.Subscribe.EventID)
	case ReplyPublish:
	case ReplyAck:
		if len(r.Ack.Payload) != 0 {
			dst = append(dst, 1)
			dst = bbAppendBytes(dst, r.Ack.Payload)
		} else {
			dst = append(dst, 0)
		}
	}
	return dst
}

func bbPushFrameSize(p *Push) int {
	n := 1 + 1
	switch p.Kind {
	case PushEvent:
		e := &p.Event
		f := bbEventPushFlags(e)
		n++
		if f&bbBit0 != 0 {
			n += 4
		}
		if f&bbBit1 != 0 {
			n += bbBytesSize(e.Room)
		}
		if f&bbBit2 != 0 {
			n += 4
		}
		if f&bbBit3 != 0 {
			n += bbBytesSize(e.Payload)
		}
		if f&bbBit4 != 0 {
			n += 8
		}
		if f&bbBit5 != 0 {
			n += 8
		}
	case PushMessage:
		m := &p.Message
		f := bbMessagePushFlags(m)
		n++
		if f&bbBit0 != 0 {
			n += 4
		}
		if f&bbBit1 != 0 {
			n += bbBytesSize(m.Payload)
		}
		if f&bbBit2 != 0 {
			n += 8
		}
	case PushJoin, PushLeave:
		n += 4
	case PushDisconnect:
		n += 4
		if len(p.Disconnect.Reason) != 0 {
			n += 1 + bbBytesSize(p.Disconnect.Reason)
		} else {
			n++
		}
	case PushRequest:
		n += 8 + 4
		if len(p.Request.Payload) != 0 {
			n += 1 + bbBytesSize(p.Request.Payload)
		} else {
			n++
		}
	}
	return n
}

func bbAppendPush(dst []byte, p *Push) []byte {
	dst = append(dst, byte(p.Kind))
	switch p.Kind {
	case PushEvent:
		e := &p.Event
		f := bbEventPushFlags(e)
		dst = append(dst, f)
		if f&bbBit0 != 0 {
			dst = bbAppendU32(dst, e.RoomID)
		}
		if f&bbBit1 != 0 {
			dst = bbAppendBytes(dst, e.Room)
		}
		if f&bbBit2 != 0 {
			dst = bbAppendU32(dst, e.EventID)
		}
		if f&bbBit3 != 0 {
			dst = bbAppendBytes(dst, e.Payload)
		}
		if f&bbBit4 != 0 {
			dst = bbAppendU64(dst, e.RequestID)
		}
		if f&bbBit5 != 0 {
			dst = bbAppendU64(dst, e.Offset)
		}
	case PushMessage:
		m := &p.Message
		f := bbMessagePushFlags(m)
		dst = append(dst, f)
		if f&bbBit0 != 0 {
			dst = bbAppendU32(dst, m.EventID)
		}
		if f&bbBit1 != 0 {
			dst = bbAppendBytes(dst, m.Payload)
		}
		if f&bbBit2 != 0 {
			dst = bbAppendU64(dst, m.RequestID)
		}
	case PushJoin:
		dst = bbAppendU32(dst, p.Join.RoomID)
	case PushLeave:
		dst = bbAppendU32(dst, p.Leave.RoomID)
	case PushDisconnect:
		dst = bbAppendU32(dst, p.Disconnect.Code)
		if len(p.Disconnect.Reason) != 0 {
			dst = append(dst, 1)
			dst = bbAppendBytes(dst, p.Disconnect.Reason)
		} else {
			dst = append(dst, 0)
		}
	case PushRequest:
		dst = bbAppendU64(dst, p.Request.Token)
		dst = bbAppendU32(dst, p.Request.EventID)
		if len(p.Request.Payload) != 0 {
			dst = append(dst, 1)
			dst = bbAppendBytes(dst, p.Request.Payload)
		} else {
			dst = append(dst, 0)
		}
	}
	return dst
}

// DecodeBebopCommands splits one WebSocket binary message into Bebop commands.
// Payloads, room names, and event names alias frame.
func DecodeBebopCommands(frame []byte, dst []Command) ([]Command, error) {
	if len(frame) == 0 {
		return dst[:0], ErrFraming
	}
	if len(frame) > MaxFrameBytes {
		return dst[:0], ErrMessageTooLarge
	}
	dst = dst[:0]
	for len(frame) > 0 {
		l, n := binary.Uvarint(frame)
		if n <= 0 {
			return dst, ErrFraming
		}
		frame = frame[n:]
		if l > uint64(len(frame)) {
			return dst, ErrFraming
		}
		msg := frame[:l]
		frame = frame[l:]
		if len(msg) > MaxMessageBytes {
			return dst, ErrMessageTooLarge
		}
		if len(dst) >= MaxBatchMessages {
			return dst, ErrTooManyMessages
		}
		var c Command
		if err := bbDecodeCommand(msg, &c); err != nil {
			return dst, err
		}
		dst = append(dst, c)
	}
	return dst, nil
}

func bbDecodeCommand(b []byte, c *Command) error {
	i := 0
	id, i, ok := bbReadU32(b, i)
	if !ok {
		return ErrWire
	}
	kind, i, ok := bbReadU8(b, i)
	if !ok {
		return ErrWire
	}
	c.ID = id
	c.Kind = CommandKind(kind)
	switch c.Kind {
	case CommandNone:
	case CommandConnect:
		f, ni, ok := bbReadU8(b, i)
		if !ok {
			return ErrWire
		}
		i = ni
		if f&bbBit0 != 0 {
			if c.Connect.Token, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit1 != 0 {
			if c.Connect.Client, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit2 != 0 {
			if c.Connect.Version, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit3 != 0 {
			count, ni, ok := bbReadU32(b, i)
			if !ok {
				return ErrWire
			}
			i = ni
			for j := uint32(0); j < count; j++ {
				v, ni, ok := bbReadBytes(b, i)
				if !ok {
					return ErrWire
				}
				c.Connect.Subscriptions = append(c.Connect.Subscriptions, v)
				i = ni
			}
		}
	case CommandSubscribe:
		if c.Subscribe.Room, i, ok = bbReadBytes(b, i); !ok {
			return ErrWire
		}
	case CommandUnsubscribe:
		if c.Unsubscribe.RoomID, i, ok = bbReadU32(b, i); !ok {
			return ErrWire
		}
	case CommandPublish:
		p := &c.Publish
		f, ni, ok := bbReadU8(b, i)
		if !ok {
			return ErrWire
		}
		i = ni
		if f&bbBit0 != 0 {
			if p.RoomID, i, ok = bbReadU32(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit1 != 0 {
			if p.Room, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit2 != 0 {
			if p.Event.ID, i, ok = bbReadU32(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit3 != 0 {
			if p.Event.Name, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit4 != 0 {
			if p.Payload, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit5 != 0 {
			if p.RequestID, i, ok = bbReadU64(b, i); !ok {
				return ErrWire
			}
		}
	case CommandSend:
		s := &c.Send
		f, ni, ok := bbReadU8(b, i)
		if !ok {
			return ErrWire
		}
		i = ni
		if f&bbBit0 != 0 {
			if s.Event.ID, i, ok = bbReadU32(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit1 != 0 {
			if s.Event.Name, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit2 != 0 {
			if s.Payload, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit3 != 0 {
			if s.RequestID, i, ok = bbReadU64(b, i); !ok {
				return ErrWire
			}
		}
	case CommandAck:
		if c.Ack.Token, i, ok = bbReadU64(b, i); !ok {
			return ErrWire
		}
		f, ni, ok := bbReadU8(b, i)
		if !ok {
			return ErrWire
		}
		i = ni
		if f&bbBit0 != 0 {
			if c.Ack.Payload, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
	case CommandPong:
	default:
		return ErrWire
	}
	if i != len(b) {
		return ErrWire
	}
	return nil
}

// DecodeBebopReplies splits one server frame into Bebop replies.
func DecodeBebopReplies(frame []byte, dst []Reply) ([]Reply, error) {
	dst = dst[:0]
	for len(frame) > 0 {
		l, n := binary.Uvarint(frame)
		if n <= 0 {
			return dst, ErrFraming
		}
		frame = frame[n:]
		if l > uint64(len(frame)) {
			return dst, ErrFraming
		}
		msg := frame[:l]
		frame = frame[l:]
		if len(msg) == 0 {
			return dst, ErrWire
		}
		var r Reply
		switch msg[0] {
		case bbFrameReply:
			if err := bbDecodeReply(msg[1:], &r); err != nil {
				return dst, err
			}
		case bbFramePush:
			r.Kind = ReplyPush
			if err := bbDecodePush(msg[1:], &r.Push); err != nil {
				return dst, err
			}
		default:
			return dst, ErrWire
		}
		dst = append(dst, r)
	}
	return dst, nil
}

func bbDecodeReply(b []byte, r *Reply) error {
	i := 0
	id, i, ok := bbReadU32(b, i)
	if !ok {
		return ErrWire
	}
	kind, i, ok := bbReadU8(b, i)
	if !ok {
		return ErrWire
	}
	flags, i, ok := bbReadU8(b, i)
	if !ok {
		return ErrWire
	}
	r.ID = id
	r.Kind = ReplyKind(kind)
	if flags&bbBit0 != 0 {
		var e Error
		if e.Code, i, ok = bbReadU32(b, i); !ok {
			return ErrWire
		}
		if e.Message, i, ok = bbReadBytes(b, i); !ok {
			return ErrWire
		}
		e.Temporary = flags&bbBit1 != 0
		r.Error = &e
	}
	switch r.Kind {
	case ReplyNone:
	case ReplyConnect:
		f, ni, ok := bbReadU8(b, i)
		if !ok {
			return ErrWire
		}
		i = ni
		if r.Connect.PingSeconds, i, ok = bbReadU32(b, i); !ok {
			return ErrWire
		}
		r.Connect.Pong = f&bbBit1 != 0
		if f&bbBit0 != 0 {
			if r.Connect.ClientID, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit2 != 0 {
			count, ni, ok := bbReadU32(b, i)
			if !ok {
				return ErrWire
			}
			i = ni
			for j := uint32(0); j < count; j++ {
				var s SubscribeResult
				if s.RoomID, i, ok = bbReadU32(b, i); !ok {
					return ErrWire
				}
				if s.EventID, i, ok = bbReadU32(b, i); !ok {
					return ErrWire
				}
				r.Connect.Subscriptions = append(r.Connect.Subscriptions, s)
			}
		}
	case ReplySubscribe:
		if r.Subscribe.RoomID, i, ok = bbReadU32(b, i); !ok {
			return ErrWire
		}
		if r.Subscribe.EventID, i, ok = bbReadU32(b, i); !ok {
			return ErrWire
		}
	case ReplyPublish:
	case ReplyAck:
		f, ni, ok := bbReadU8(b, i)
		if !ok {
			return ErrWire
		}
		i = ni
		if f&bbBit0 != 0 {
			if r.Ack.Payload, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
	case ReplyPush:
		return ErrWire
	default:
		return ErrWire
	}
	if i != len(b) {
		return ErrWire
	}
	return nil
}

func bbDecodePush(b []byte, p *Push) error {
	i := 0
	kind, i, ok := bbReadU8(b, i)
	if !ok {
		return ErrWire
	}
	p.Kind = PushKind(kind)
	switch p.Kind {
	case PushNone:
	case PushEvent:
		e := &p.Event
		f, ni, ok := bbReadU8(b, i)
		if !ok {
			return ErrWire
		}
		i = ni
		if f&bbBit0 != 0 {
			if e.RoomID, i, ok = bbReadU32(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit1 != 0 {
			if e.Room, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit2 != 0 {
			if e.EventID, i, ok = bbReadU32(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit3 != 0 {
			if e.Payload, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit4 != 0 {
			if e.RequestID, i, ok = bbReadU64(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit5 != 0 {
			if e.Offset, i, ok = bbReadU64(b, i); !ok {
				return ErrWire
			}
		}
	case PushMessage:
		m := &p.Message
		f, ni, ok := bbReadU8(b, i)
		if !ok {
			return ErrWire
		}
		i = ni
		if f&bbBit0 != 0 {
			if m.EventID, i, ok = bbReadU32(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit1 != 0 {
			if m.Payload, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
		if f&bbBit2 != 0 {
			if m.RequestID, i, ok = bbReadU64(b, i); !ok {
				return ErrWire
			}
		}
	case PushJoin:
		if p.Join.RoomID, i, ok = bbReadU32(b, i); !ok {
			return ErrWire
		}
	case PushLeave:
		if p.Leave.RoomID, i, ok = bbReadU32(b, i); !ok {
			return ErrWire
		}
	case PushDisconnect:
		if p.Disconnect.Code, i, ok = bbReadU32(b, i); !ok {
			return ErrWire
		}
		f, ni, ok := bbReadU8(b, i)
		if !ok {
			return ErrWire
		}
		i = ni
		if f&bbBit0 != 0 {
			if p.Disconnect.Reason, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
	case PushRequest:
		if p.Request.Token, i, ok = bbReadU64(b, i); !ok {
			return ErrWire
		}
		if p.Request.EventID, i, ok = bbReadU32(b, i); !ok {
			return ErrWire
		}
		f, ni, ok := bbReadU8(b, i)
		if !ok {
			return ErrWire
		}
		i = ni
		if f&bbBit0 != 0 {
			if p.Request.Payload, i, ok = bbReadBytes(b, i); !ok {
				return ErrWire
			}
		}
	default:
		return ErrWire
	}
	if i != len(b) {
		return ErrWire
	}
	return nil
}
