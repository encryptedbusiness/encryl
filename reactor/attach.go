package reactor

// AttachSpec is the validated upgrade result for a connection that the
// framework has already parsed.
type AttachSpec struct {
	Accept         [28]byte
	Subprotocol    [32]byte
	SubprotocolLen uint8
	Encoding       uint8
}

// AttachCommand hands an accepted, already-upgraded socket to a loop. It is
// deliberately separate from Command: attachment is rare and the command ring
// is on the send/fan-out hot path.
type AttachCommand struct {
	FD   int
	Spec AttachSpec
	Data []byte
}

// AttachRing is a small fixed-capacity MPSC queue for handoffs.
type AttachRing struct{ *mpscRing[AttachCommand] }

// NewAttachRing returns a ring with the given capacity.
func NewAttachRing(capacity int) *AttachRing {
	return &AttachRing{mpscRing: newMPSCRing[AttachCommand](capacity, 256)}
}
