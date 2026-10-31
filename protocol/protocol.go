package protocol

import "errors"

// Encoding identifies the wire encoding of a connection.
type Encoding uint8

const (
	// EncodingNative is the encryl.v2 16-byte envelope.
	EncodingNative Encoding = iota
	// EncodingBebopV1 is the only client event codec. It uses fixed-width
	// Bebop values with a presence bitmap.
	EncodingBebopV1
)

// SubprotocolBebopV1 is the WebSocket subprotocol for the event codec.
const SubprotocolBebopV1 = "encryl.bebop.v1"

// Wire limits. The server applies its own Config limits as well.
const (
	// MaxFrameBytes bounds one WebSocket binary message.
	MaxFrameBytes = 4 << 20
	// MaxMessageBytes bounds one protocol message inside a frame.
	MaxMessageBytes = 1 << 20
	// MaxBatchMessages bounds the number of messages in one frame.
	MaxBatchMessages = 1024
)

// Error codes carried in Reply.error. They are deliberately small and stable.
const (
	CodeInternal      uint32 = 1
	CodeUnauthorized  uint32 = 2
	CodeUnknownRoom   uint32 = 3
	CodePermission    uint32 = 4
	CodeUnknownEvent  uint32 = 5
	CodeLimitExceeded uint32 = 7
	CodeBackpressure  uint32 = 8
	CodeBadRequest    uint32 = 9
	CodeUnsupported   uint32 = 10
	CodeNotSubscribed uint32 = 11
)

var (
	// ErrFraming reports a malformed length prefix or a truncated message.
	ErrFraming = errors.New("protocol: malformed frame")
	// ErrMessageTooLarge reports a message above MaxMessageBytes.
	ErrMessageTooLarge = errors.New("protocol: message too large")
	// ErrTooManyMessages reports a batch above MaxBatchMessages.
	ErrTooManyMessages = errors.New("protocol: too many messages")
	// ErrWire reports a malformed field.
	ErrWire = errors.New("protocol: malformed message")
)
