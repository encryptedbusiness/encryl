package app

import (
	"errors"
	"fmt"
)

// Public errors returned by Server, Conn, and Room operations.
//
// All of them are comparable with errors.Is. Errors that describe a rejected
// operation rather than a broken server are values, not wrapped strings, so
// callers can branch on them without parsing messages.
var (
	// ErrClosed is returned when the Server or Conn has begun closing.
	ErrClosed = errors.New("encryl: closed")

	// ErrBackpressure is returned by Send when the connection's write queue
	// is full and the configured policy rejects the message. The message was
	// not queued and no bytes were written.
	ErrBackpressure = errors.New("encryl: write queue full")

	// ErrSlowConsumer is returned by Send after the connection was closed for
	// exceeding its write budget under BackpressureCloseSlow.
	ErrSlowConsumer = errors.New("encryl: connection closed as slow consumer")

	// ErrPayloadTooLarge is returned when a message exceeds MaxMessageSize.
	ErrPayloadTooLarge = errors.New("encryl: payload too large")

	// ErrUnauthorized is returned when an event is dispatched before the
	// connection completed authentication, or the handler rejected it.
	ErrUnauthorized = errors.New("encryl: unauthorized")

	// ErrRoomLimit is returned by Conn.Join when MaxRoomsPerConn is reached.
	ErrRoomLimit = errors.New("encryl: room membership limit reached")

	// ErrUnsupported is returned when the request needs a backend or platform
	// feature that is not available, for example the io_uring backend on
	// macOS or TLS kernel offload on a kernel without the tls module.
	ErrUnsupported = errors.New("encryl: unsupported on this platform")

	// ErrAckTimeout is delivered to an EmitWithAck callback when no
	// acknowledgement arrived before the deadline.
	ErrAckTimeout = errors.New("encryl: acknowledgement timeout")

	// ErrInvalidUpgrade is returned by Upgrade when the UpgradeSpec did not
	// come from PrepareUpgrade.
	ErrInvalidUpgrade = errors.New("encryl: invalid upgrade spec")
)

// ConfigProblem is one typed validation result. Field is the Config field,
// Rule is the stable rule name, and Message is the readable explanation.
type ConfigProblem struct {
	Field   string
	Value   any
	Rule    string
	Message string
}

// ConfigError reports invalid Config fields and non-fatal warnings.
type ConfigError struct {
	Problems []ConfigProblem
	Warnings []ConfigProblem
}

func (e *ConfigError) Error() string {
	if len(e.Problems) == 1 {
		return "encryl: invalid config: " + e.Problems[0].Message
	}
	msgs := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		msgs[i] = p.Message
	}
	return fmt.Sprintf("encryl: invalid config (%d problems): %v", len(e.Problems), msgs)
}
