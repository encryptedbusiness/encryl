package reactor

import connection "github.com/encryptedbusiness/encryl/connslab"

// CommandKind identifies cross-loop work.
type CommandKind uint8

const (
	CmdInvalid CommandKind = iota
	CmdSend
	CmdClose
	CmdBroadcast
	CmdFanout
	CmdAckArm
	CmdAckCancel
)

// Command is one unit of cross-loop work. Frame and Refs are owned by the
// producer and must remain valid until the owning loop processes the command.
type Command struct {
	Kind          CommandKind
	Slot          uint32
	Gen           uint32
	A             uint64
	B             uint64
	Data          []byte
	Frame         *connection.Frame
	Batch         *connection.Batch
	Except        []connection.Ref
	SelfExcept    connection.Ref
	HasSelfExcept bool
	// Encoding filters a broadcast: only connections with this encoding
	// receive the frame. 0 is native, 1 is Bebop.
	Encoding uint8
}

// CommandRing is a fixed-capacity MPSC queue for cross-loop work.
type CommandRing struct{ *mpscRing[Command] }

// NewCommandRing returns a ring with the given capacity.
func NewCommandRing(capacity int) *CommandRing {
	return &CommandRing{mpscRing: newMPSCRing[Command](capacity, 1024)}
}
