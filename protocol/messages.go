package protocol

// CommandKind identifies the request in a Command.
type CommandKind uint8

const (
	CommandNone CommandKind = iota
	CommandConnect
	CommandSubscribe
	CommandUnsubscribe
	CommandPublish
	CommandSend
	CommandAck
	CommandPong
)

func (k CommandKind) String() string {
	switch k {
	case CommandConnect:
		return "connect"
	case CommandSubscribe:
		return "subscribe"
	case CommandUnsubscribe:
		return "unsubscribe"
	case CommandPublish:
		return "publish"
	case CommandSend:
		return "send"
	case CommandAck:
		return "ack"
	case CommandPong:
		return "pong"
	default:
		return "none"
	}
}

// Command is one client-to-server request. Fields alias the frame buffer when
// decoded, except the subscription list which is control-path.
type Command struct {
	ID          uint32
	Kind        CommandKind
	Connect     Connect
	Subscribe   Subscribe
	Unsubscribe Unsubscribe
	Publish     Publish
	Send        Send
	Ack         Ack
}

// Connect opens a protocol session.
type Connect struct {
	Token         []byte
	Client        []byte
	Version       []byte
	Subscriptions [][]byte
}

// Subscribe joins a named room.
type Subscribe struct {
	Room []byte
}

// Unsubscribe leaves a room by its handle.
type Unsubscribe struct {
	RoomID uint32
}

// EventRef identifies an event by numeric ID or by name. The numeric form is
// the hot path; the name form is resolved and interned by the server.
type EventRef struct {
	ID   uint32
	Name []byte
}

// Publish sends an event to a room. RoomID is the hot path; Room is for the
// first subscribe/publish or dynamic rooms.
type Publish struct {
	RoomID    uint32
	Room      []byte
	Event     EventRef
	Payload   []byte
	RequestID uint64
}

// Send dispatches an event without a reply.
type Send struct {
	Event     EventRef
	Payload   []byte
	RequestID uint64
}

// Ack answers a server-initiated request.
type Ack struct {
	Token   uint64
	Payload []byte
}

// ReplyKind identifies the body of a Reply.
type ReplyKind uint8

const (
	ReplyNone ReplyKind = iota
	ReplyConnect
	ReplySubscribe
	ReplyPublish
	ReplyAck
	ReplyPush
)

// Reply is one server-to-client message. ID is zero for pushes.
type Reply struct {
	ID        uint32
	Kind      ReplyKind
	Error     *Error
	Connect   ConnectResult
	Subscribe SubscribeResult
	Ack       AckResult
	Push      Push
}

// Error is a failed command result.
type Error struct {
	Code      uint32
	Message   []byte
	Temporary bool
}

// ConnectResult is the outcome of a successful connect.
type ConnectResult struct {
	ClientID      []byte
	PingSeconds   uint32
	Pong          bool
	Subscriptions []SubscribeResult
}

// SubscribeResult is the outcome of a successful subscribe.
type SubscribeResult struct {
	RoomID  uint32
	EventID uint32
}

// AckResult carries an application reply to a client command.
type AckResult struct {
	Payload []byte
}

// PushKind identifies an asynchronous server message.
type PushKind uint8

const (
	PushNone PushKind = iota
	PushEvent
	PushMessage
	PushJoin
	PushLeave
	PushDisconnect
	PushRequest
)

// Push is an asynchronous server message. A push travels inside a Reply with
// ID zero.
type Push struct {
	Kind       PushKind
	Event      EventPush
	Message    Message
	Join       Join
	Leave      Leave
	Disconnect Disconnect
	Request    Request
}

// EventPush is an event delivered to subscribed room members.
type EventPush struct {
	RoomID    uint32
	Room      []byte
	EventID   uint32
	Payload   []byte
	RequestID uint64
	Offset    uint64
}

// Message is a Self-Room (unicast) event that is not tied to a room.
type Message struct {
	EventID   uint32
	Payload   []byte
	RequestID uint64
}

// Join reports a new room member.
type Join struct {
	RoomID uint32
}

// Leave reports a departed room member.
type Leave struct {
	RoomID uint32
}

// Disconnect tells the client to close the connection.
type Disconnect struct {
	Code   uint32
	Reason []byte
}

// Request is a server-initiated request that expects Command.ack.
type Request struct {
	Token   uint64
	EventID uint32
	Payload []byte
}
