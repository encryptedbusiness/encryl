package protocol

import "encoding/binary"

// DecodeCommands splits one WebSocket binary message into commands. Payloads,
// room names, event names, and tokens alias frame. The caller must handle the
// commands before the frame is reused or released.
func DecodeCommands(frame []byte, dst []Command) ([]Command, error) {
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
		if err := decodeCommand(msg, &c); err != nil {
			return dst, err
		}
		dst = append(dst, c)
	}
	return dst, nil
}

// DecodeReplies splits one server frame into replies. It exists for tests and
// client implementations.
func DecodeReplies(frame []byte, dst []Reply) ([]Reply, error) {
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
		var r Reply
		if err := decodeReply(msg, &r); err != nil {
			return dst, err
		}
		dst = append(dst, r)
	}
	return dst, nil
}
