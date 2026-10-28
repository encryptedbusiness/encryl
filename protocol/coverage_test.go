package protocol

import (
	"encoding/binary"
	"errors"
	"testing"
)

func TestEveryCommandKindRoundTrips(t *testing.T) {
	tests := []struct {
		name  string
		in    Command
		check func(*testing.T, Command)
	}{
		{
			name: "connect",
			in: Command{ID: 1, Kind: CommandConnect, Connect: Connect{
				Token: []byte("t"), Client: []byte("c"), Version: []byte("v"),
				Subscriptions: [][]byte{[]byte("a"), []byte("b")},
			}},
			check: func(t *testing.T, got Command) {
				c := got.Connect
				if string(c.Token) != "t" || string(c.Client) != "c" || string(c.Version) != "v" ||
					len(c.Subscriptions) != 2 || string(c.Subscriptions[1]) != "b" {
					t.Fatalf("connect = %+v", c)
				}
			},
		},
		{
			name: "subscribe",
			in:   Command{ID: 2, Kind: CommandSubscribe, Subscribe: Subscribe{Room: []byte("room")}},
			check: func(t *testing.T, got Command) {
				if string(got.Subscribe.Room) != "room" {
					t.Fatalf("subscribe = %+v", got.Subscribe)
				}
			},
		},
		{
			name: "unsubscribe",
			in:   Command{ID: 3, Kind: CommandUnsubscribe, Unsubscribe: Unsubscribe{RoomID: 9}},
			check: func(t *testing.T, got Command) {
				if got.Unsubscribe.RoomID != 9 {
					t.Fatalf("unsubscribe = %+v", got.Unsubscribe)
				}
			},
		},
		{
			name: "publish by room id",
			in: Command{ID: 4, Kind: CommandPublish, Publish: Publish{
				RoomID: 9, Event: EventRef{ID: 7}, Payload: []byte("p"), RequestID: 11,
			}},
			check: func(t *testing.T, got Command) {
				p := got.Publish
				if p.RoomID != 9 || p.Event.ID != 7 || string(p.Payload) != "p" || p.RequestID != 11 {
					t.Fatalf("publish = %+v", p)
				}
			},
		},
		{
			name: "publish by room name",
			in: Command{Kind: CommandPublish, Publish: Publish{
				Room: []byte("chat"), Event: EventRef{Name: []byte("chat.message")}, Payload: []byte("x"),
			}},
			check: func(t *testing.T, got Command) {
				p := got.Publish
				if string(p.Room) != "chat" || string(p.Event.Name) != "chat.message" || string(p.Payload) != "x" {
					t.Fatalf("publish = %+v", p)
				}
			},
		},
		{
			name: "send by name",
			in: Command{ID: 5, Kind: CommandSend, Send: Send{
				Event: EventRef{Name: []byte("chat")}, Payload: []byte("s"), RequestID: 12,
			}},
			check: func(t *testing.T, got Command) {
				s := got.Send
				if string(s.Event.Name) != "chat" || string(s.Payload) != "s" || s.RequestID != 12 {
					t.Fatalf("send = %+v", s)
				}
			},
		},
		{
			name: "ack",
			in:   Command{ID: 6, Kind: CommandAck, Ack: Ack{Token: 42, Payload: []byte("ok")}},
			check: func(t *testing.T, got Command) {
				if got.Ack.Token != 42 || string(got.Ack.Payload) != "ok" {
					t.Fatalf("ack = %+v", got.Ack)
				}
			},
		},
		{
			name:  "pong",
			in:    Command{ID: 7, Kind: CommandPong},
			check: func(t *testing.T, got Command) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := AppendFramedCommand(nil, &tt.in)
			cmds, err := DecodeCommands(frame, nil)
			if err != nil {
				t.Fatalf("DecodeCommands: %v", err)
			}
			if len(cmds) != 1 {
				t.Fatalf("commands = %d", len(cmds))
			}
			if cmds[0].Kind != tt.in.Kind || cmds[0].ID != tt.in.ID {
				t.Fatalf("header = kind %d id %d, want %d/%d", cmds[0].Kind, cmds[0].ID, tt.in.Kind, tt.in.ID)
			}
			tt.check(t, cmds[0])
		})
	}
}

func TestEveryReplyKindRoundTrips(t *testing.T) {
	tests := []struct {
		name  string
		in    Reply
		check func(*testing.T, Reply)
	}{
		{
			name: "error",
			in: Reply{ID: 1, Error: &Error{
				Code: CodeBadRequest, Message: []byte("bad"), Temporary: true,
			}},
			check: func(t *testing.T, got Reply) {
				if got.Error == nil || got.Error.Code != CodeBadRequest ||
					string(got.Error.Message) != "bad" || !got.Error.Temporary {
					t.Fatalf("error = %+v", got.Error)
				}
			},
		},
		{
			name: "subscribe",
			in:   Reply{ID: 2, Kind: ReplySubscribe, Subscribe: SubscribeResult{RoomID: 3, EventID: 4}},
			check: func(t *testing.T, got Reply) {
				if got.Subscribe.RoomID != 3 || got.Subscribe.EventID != 4 {
					t.Fatalf("subscribe = %+v", got.Subscribe)
				}
			},
		},
		{
			name: "publish",
			in:   Reply{ID: 3, Kind: ReplyPublish},
			check: func(t *testing.T, got Reply) {
			},
		},
		{
			name: "ack",
			in:   Reply{ID: 4, Kind: ReplyAck, Ack: AckResult{Payload: []byte("ok")}},
			check: func(t *testing.T, got Reply) {
				if string(got.Ack.Payload) != "ok" {
					t.Fatalf("ack = %+v", got.Ack)
				}
			},
		},
		{
			name: "push event",
			in: Reply{Kind: ReplyPush, Push: Push{Kind: PushEvent, Event: EventPush{
				RoomID: 5, Room: []byte("room"), EventID: 6, Payload: []byte("e"),
				RequestID: 7, Offset: 8,
			}}},
			check: func(t *testing.T, got Reply) {
				e := got.Push.Event
				if e.RoomID != 5 || string(e.Room) != "room" || e.EventID != 6 ||
					string(e.Payload) != "e" || e.RequestID != 7 || e.Offset != 8 {
					t.Fatalf("event = %+v", e)
				}
			},
		},
		{
			name: "push message",
			in: Reply{Kind: ReplyPush, Push: Push{Kind: PushMessage, Message: Message{
				EventID: 9, Payload: []byte("m"), RequestID: 10,
			}}},
			check: func(t *testing.T, got Reply) {
				m := got.Push.Message
				if m.EventID != 9 || string(m.Payload) != "m" || m.RequestID != 10 {
					t.Fatalf("message = %+v", m)
				}
			},
		},
		{
			name: "push join",
			in:   Reply{Kind: ReplyPush, Push: Push{Kind: PushJoin, Join: Join{RoomID: 11}}},
			check: func(t *testing.T, got Reply) {
				if got.Push.Join.RoomID != 11 {
					t.Fatalf("join = %+v", got.Push.Join)
				}
			},
		},
		{
			name: "push leave",
			in:   Reply{Kind: ReplyPush, Push: Push{Kind: PushLeave, Leave: Leave{RoomID: 12}}},
			check: func(t *testing.T, got Reply) {
				if got.Push.Leave.RoomID != 12 {
					t.Fatalf("leave = %+v", got.Push.Leave)
				}
			},
		},
		{
			name: "push disconnect",
			in: Reply{Kind: ReplyPush, Push: Push{Kind: PushDisconnect, Disconnect: Disconnect{
				Code: 1000, Reason: []byte("bye"),
			}}},
			check: func(t *testing.T, got Reply) {
				d := got.Push.Disconnect
				if d.Code != 1000 || string(d.Reason) != "bye" {
					t.Fatalf("disconnect = %+v", d)
				}
			},
		},
		{
			name: "push request",
			in: Reply{Kind: ReplyPush, Push: Push{Kind: PushRequest, Request: Request{
				Token: 13, EventID: 14, Payload: []byte("r"),
			}}},
			check: func(t *testing.T, got Reply) {
				r := got.Push.Request
				if r.Token != 13 || r.EventID != 14 || string(r.Payload) != "r" {
					t.Fatalf("request = %+v", r)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := AppendFramedReply(nil, &tt.in)
			replies, err := DecodeReplies(frame, nil)
			if err != nil {
				t.Fatalf("DecodeReplies: %v", err)
			}
			if len(replies) != 1 {
				t.Fatalf("replies = %d", len(replies))
			}
			if replies[0].ID != tt.in.ID || replies[0].Kind != tt.in.Kind {
				t.Fatalf("header = id %d kind %d, want %d/%d",
					replies[0].ID, replies[0].Kind, tt.in.ID, tt.in.Kind)
			}
			tt.check(t, replies[0])
		})
	}
}

func TestFramedSizeHelpersMatchEncoders(t *testing.T) {
	commands := []Command{
		{ID: 1, Kind: CommandConnect, Connect: Connect{Token: []byte("token"), Subscriptions: [][]byte{[]byte("a")}}},
		{ID: 2, Kind: CommandSubscribe, Subscribe: Subscribe{Room: []byte("room")}},
		{ID: 3, Kind: CommandUnsubscribe, Unsubscribe: Unsubscribe{RoomID: 1234}},
		{ID: 4, Kind: CommandPublish, Publish: Publish{Room: []byte("room"), Event: EventRef{ID: 1}, Payload: []byte("p")}},
		{ID: 5, Kind: CommandSend, Send: Send{Event: EventRef{Name: []byte("e")}, Payload: []byte("p")}},
		{ID: 6, Kind: CommandAck, Ack: Ack{Token: 99, Payload: []byte("a")}},
		{ID: 7, Kind: CommandPong},
	}
	for i := range commands {
		c := &commands[i]
		if got, want := len(AppendFramedCommand(nil, c)), FramedCommandSize(c); got != want {
			t.Errorf("command %d size = %d, want %d", i, got, want)
		}
	}

	replies := []Reply{
		{ID: 1, Error: &Error{Code: CodeInternal, Message: []byte("x"), Temporary: true}},
		{ID: 2, Kind: ReplyConnect, Connect: ConnectResult{
			ClientID: []byte("c"), PingSeconds: 25, Pong: true,
			Subscriptions: []SubscribeResult{{RoomID: 1, EventID: 2}},
		}},
		{ID: 3, Kind: ReplySubscribe, Subscribe: SubscribeResult{RoomID: 1, EventID: 2}},
		{ID: 4, Kind: ReplyPublish},
		{ID: 5, Kind: ReplyAck, Ack: AckResult{Payload: []byte("a")}},
		{Kind: ReplyPush, Push: Push{Kind: PushEvent, Event: EventPush{Room: []byte("r"), Payload: []byte("p")}}},
	}
	for i := range replies {
		r := &replies[i]
		if got, want := len(AppendFramedReply(nil, r)), FramedReplySize(r); got != want {
			t.Errorf("reply %d size = %d, want %d", i, got, want)
		}
	}

	pushes := []Push{
		{Kind: PushEvent, Event: EventPush{Room: []byte("r"), Payload: []byte("p")}},
		{Kind: PushMessage, Message: Message{EventID: 1, Payload: []byte("p")}},
		{Kind: PushJoin, Join: Join{RoomID: 1}},
		{Kind: PushLeave, Leave: Leave{RoomID: 1}},
		{Kind: PushDisconnect, Disconnect: Disconnect{Code: 1000, Reason: []byte("bye")}},
		{Kind: PushRequest, Request: Request{Token: 1, EventID: 2, Payload: []byte("p")}},
	}
	for i := range pushes {
		p := &pushes[i]
		if got, want := len(AppendFramedPush(nil, p)), FramedPushSize(p); got != want {
			t.Errorf("push %d size = %d, want %d", i, got, want)
		}
		if replies, err := DecodeReplies(AppendFramedPush(nil, p), nil); err != nil || len(replies) != 1 {
			t.Errorf("push %d round trip = %d/%v", i, len(replies), err)
		}
	}
}

func TestDecodeRejectsMalformedFrames(t *testing.T) {
	if _, err := DecodeCommands(nil, nil); !errors.Is(err, ErrFraming) {
		t.Fatalf("empty frame = %v, want ErrFraming", err)
	}
	if _, err := DecodeCommands([]byte{0x80}, nil); !errors.Is(err, ErrFraming) {
		t.Fatalf("truncated varint = %v, want ErrFraming", err)
	}
	if _, err := DecodeCommands([]byte{0x05, 0x01}, nil); !errors.Is(err, ErrFraming) {
		t.Fatalf("truncated body = %v, want ErrFraming", err)
	}
	if _, err := DecodeCommands([]byte{0x01, 0xff}, nil); !errors.Is(err, ErrWire) {
		t.Fatalf("malformed protobuf = %v, want ErrWire", err)
	}
}

func TestDecodeEnforcesFrameAndMessageLimits(t *testing.T) {
	oversized := make([]byte, MaxFrameBytes+1)
	if _, err := DecodeCommands(oversized, nil); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("oversized frame = %v, want ErrMessageTooLarge", err)
	}

	// A message at MaxMessageBytes+1 inside a legal frame.
	msg := make([]byte, MaxMessageBytes+1)
	frame := make([]byte, binary.MaxVarintLen64+len(msg))
	n := binary.PutUvarint(frame, uint64(len(msg)))
	copy(frame[n:], msg)
	frame = frame[:n+len(msg)]
	if _, err := DecodeCommands(frame, nil); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("oversized message = %v, want ErrMessageTooLarge", err)
	}

	// One more message than the batch limit.
	var batched []byte
	pong := Command{Kind: CommandPong}
	for i := 0; i < MaxBatchMessages+1; i++ {
		pong.ID = uint32(i)
		batched = AppendFramedCommand(batched, &pong)
	}
	if _, err := DecodeCommands(batched, nil); !errors.Is(err, ErrTooManyMessages) {
		t.Fatalf("oversized batch = %v, want ErrTooManyMessages", err)
	}
}

func TestDecodeRepliesRejectsMalformed(t *testing.T) {
	if _, err := DecodeReplies([]byte{0x80}, nil); !errors.Is(err, ErrFraming) {
		t.Fatalf("truncated varint = %v, want ErrFraming", err)
	}
	if _, err := DecodeReplies([]byte{0x05, 0x01}, nil); !errors.Is(err, ErrFraming) {
		t.Fatalf("truncated body = %v, want ErrFraming", err)
	}
	if _, err := DecodeReplies([]byte{0x01, 0xff}, nil); !errors.Is(err, ErrWire) {
		t.Fatalf("malformed protobuf = %v, want ErrWire", err)
	}
}

func TestSessionRoomHandles(t *testing.T) {
	ReleaseSession(nil)

	s := AcquireSession()
	if s.Connected() {
		t.Fatal("fresh session is connected")
	}
	s.SetConnected(true)
	if !s.Connected() {
		t.Fatal("SetConnected did not stick")
	}

	a := s.AddRoom("alpha")
	if a == 0 {
		t.Fatal("room handle is zero")
	}
	if again := s.AddRoom("alpha"); again != a {
		t.Fatalf("same room got %d and %d", a, again)
	}
	b := s.AddRoom("beta")
	if b == a {
		t.Fatal("distinct rooms share a handle")
	}
	if name, ok := s.RoomName(a); !ok || name != "alpha" {
		t.Fatalf("RoomName = %q/%v", name, ok)
	}
	if _, ok := s.RoomName(999); ok {
		t.Fatal("unknown handle resolved")
	}

	s.RemoveRoom(a)
	if _, ok := s.RoomName(a); ok {
		t.Fatal("removed room still resolves")
	}
	if again := s.AddRoom("alpha"); again == a {
		t.Fatal("removed handle was reused")
	}
	s.RemoveRoom(999) // must not panic

	frame := AppendFramedCommand(nil, &Command{ID: 3, Kind: CommandPong})
	cmds, err := s.Decode(frame)
	if err != nil || len(cmds) != 1 || cmds[0].Kind != CommandPong {
		t.Fatalf("Decode = %+v/%v", cmds, err)
	}

	ReleaseSession(s)

	// A recycled session is reset.
	r := AcquireSession()
	if r.Connected() {
		t.Fatal("recycled session kept connected state")
	}
	if _, ok := r.RoomName(b); ok {
		t.Fatal("recycled session kept room handles")
	}
	ReleaseSession(r)
}

func TestCommandKindStrings(t *testing.T) {
	tests := map[CommandKind]string{
		CommandNone:        "none",
		CommandConnect:     "connect",
		CommandSubscribe:   "subscribe",
		CommandUnsubscribe: "unsubscribe",
		CommandPublish:     "publish",
		CommandSend:        "send",
		CommandAck:         "ack",
		CommandPong:        "pong",
	}
	for k, want := range tests {
		if got := k.String(); got != want {
			t.Errorf("CommandKind(%d).String() = %q, want %q", k, got, want)
		}
	}
	if got := CommandKind(200).String(); got != "none" {
		t.Errorf("unknown CommandKind = %q", got)
	}
}
