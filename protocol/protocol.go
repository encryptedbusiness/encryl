// Package protocol implements the encryl protobuf event protocol.
//
// The protocol is a small, protobuf-only subset inspired by Centrifuge. It is
// not wire-compatible with Centrifuge or Centrifugo. Messages are framed as
// varint-length-prefixed protobuf messages concatenated in one WebSocket
// binary message. The event core (registry, rooms, Self-Rooms, fan-out) stays
// behind this package.
package protocol

import "errors"

// Encoding identifies the wire encoding of a connection.
type Encoding uint8

const (
	// EncodingNative is the encryl.v2 16-byte envelope. It is kept as a
	// compatibility codec behind the same seam.
	EncodingNative Encoding = iota
	// EncodingProtobufV1 is this package.
	EncodingProtobufV1
)

// Subprotocol names used during the WebSocket upgrade.
const (
	SubprotocolProtobufV1 = "encryl.pb.v1"
)

// Wire limits. The server applies its own Config limits as well.
const (
	// MaxFrameBytes bounds one WebSocket binary message.
	MaxFrameBytes = 4 << 20
	// MaxMessageBytes bounds one protobuf message inside a frame.
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
	// ErrWire reports a malformed protobuf field.
	ErrWire = errors.New("protocol: malformed message")
)
