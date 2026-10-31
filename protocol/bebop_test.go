package protocol

import (
	"bytes"
	"testing"
)

func TestGoldenBebopSendFrame(t *testing.T) {
	cmd := Command{ID: 1, Kind: CommandSend, Send: Send{
		Event: EventRef{ID: 1024}, Payload: []byte("hi"),
	}}
	want := []byte{
		0x10,                   // varint(16) body length
		0x01, 0x00, 0x00, 0x00, // id
		0x05,                   // CommandSend
		0x05,                   // presence: event ID + payload
		0x00, 0x04, 0x00, 0x00, // event ID 1024
		0x02, 0x00, 0x00, 0x00, // payload length 2
		'h', 'i',
	}
	if got := AppendFramedCommandBebop(nil, &cmd); !bytes.Equal(got, want) {
		t.Fatalf("frame = % x, want % x", got, want)
	}
}

func TestBebopCommandRoundTrips(t *testing.T) {
	tests := []Command{
		{ID: 1, Kind: CommandConnect, Connect: Connect{
			Token: []byte("token"), Client: []byte("go"), Version: []byte("1"),
			Subscriptions: [][]byte{[]byte("a"), []byte("b")},
		}},
		{ID: 2, Kind: CommandSubscribe, Subscribe: Subscribe{Room: []byte("room")}},
		{ID: 3, Kind: CommandUnsubscribe, Unsubscribe: Unsubscribe{RoomID: 9}},
		{ID: 4, Kind: CommandPublish, Publish: Publish{
			RoomID: 9, Room: []byte("chat"), Event: EventRef{ID: 1024, Name: []byte("chat.message")},
			Payload: []byte("hello"), RequestID: 11,
		}},
		{ID: 5, Kind: CommandSend, Send: Send{
			Event: EventRef{ID: 7, Name: []byte("chat")}, Payload: []byte("x"), RequestID: 12,
		}},
		{ID: 6, Kind: CommandAck, Ack: Ack{Token: 42, Payload: []byte("ok")}},
		{ID: 7, Kind: CommandPong},
	}
	for i := range tests {
		in := &tests[i]
		frame := AppendFramedCommandBebop(nil, in)
		if got, want := len(frame), FramedCommandSizeBebop(in); got != want {
			t.Fatalf("command %d size = %d, want %d", i, got, want)
		}
		out, err := DecodeBebopCommands(frame, nil)
		if err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
		if len(out) != 1 || out[0].ID != in.ID || out[0].Kind != in.Kind {
			t.Fatalf("command %d = %+v", i, out)
		}
		switch in.Kind {
		case CommandPublish:
			if string(out[0].Publish.Payload) != "hello" || out[0].Publish.Event.ID != 1024 {
				t.Fatalf("publish = %+v", out[0].Publish)
			}
		case CommandConnect:
			if len(out[0].Connect.Subscriptions) != 2 {
				t.Fatalf("connect = %+v", out[0].Connect)
			}
		}
	}
}

func TestBebopReplyAndPushRoundTrips(t *testing.T) {
	replies := []Reply{
		{ID: 1, Error: &Error{Code: CodeBadRequest, Message: []byte("bad"), Temporary: true}},
		{ID: 2, Kind: ReplyConnect, Connect: ConnectResult{
			ClientID: []byte("c"), PingSeconds: 25, Pong: true,
			Subscriptions: []SubscribeResult{{RoomID: 1, EventID: 2}},
		}},
		{ID: 3, Kind: ReplySubscribe, Subscribe: SubscribeResult{RoomID: 3, EventID: 4}},
		{ID: 4, Kind: ReplyPublish},
		{ID: 5, Kind: ReplyAck, Ack: AckResult{Payload: []byte("ok")}},
		{Kind: ReplyPush, Push: Push{Kind: PushEvent, Event: EventPush{
			RoomID: 5, Room: []byte("room"), EventID: 6, Payload: []byte("e"), RequestID: 7, Offset: 8,
		}}},
		{Kind: ReplyPush, Push: Push{Kind: PushMessage, Message: Message{
			EventID: 9, Payload: []byte("m"), RequestID: 10,
		}}},
		{Kind: ReplyPush, Push: Push{Kind: PushJoin, Join: Join{RoomID: 11}}},
		{Kind: ReplyPush, Push: Push{Kind: PushLeave, Leave: Leave{RoomID: 12}}},
		{Kind: ReplyPush, Push: Push{Kind: PushDisconnect, Disconnect: Disconnect{
			Code: 1000, Reason: []byte("bye"),
		}}},
		{Kind: ReplyPush, Push: Push{Kind: PushRequest, Request: Request{
			Token: 13, EventID: 14, Payload: []byte("r"),
		}}},
	}
	for i := range replies {
		in := &replies[i]
		frame := AppendFramedReplyBebop(nil, in)
		if got, want := len(frame), FramedReplySizeBebop(in); got != want {
			t.Fatalf("reply %d size = %d, want %d", i, got, want)
		}
		out, err := DecodeBebopReplies(frame, nil)
		if err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
		if len(out) != 1 || out[0].ID != in.ID || out[0].Kind != in.Kind {
			t.Fatalf("reply %d = %+v", i, out)
		}
		if in.Kind == ReplyPush && out[0].Push.Kind != in.Push.Kind {
			t.Fatalf("push %d kind = %d, want %d", i, out[0].Push.Kind, in.Push.Kind)
		}
	}
}

func TestBebopCompactFlagPaths(t *testing.T) {
	cmds := []Command{
		{ID: 1, Kind: CommandPublish, Publish: Publish{
			RoomID: 7, Event: EventRef{ID: 1024}, Payload: []byte("p"), RequestID: 9,
		}},
		{ID: 2, Kind: CommandPublish, Publish: Publish{
			Room: []byte("chat"), Event: EventRef{Name: []byte("chat.message")}, Payload: []byte("p"),
		}},
		{ID: 3, Kind: CommandSend, Send: Send{
			Event: EventRef{ID: 5}, Payload: []byte("s"), RequestID: 6,
		}},
		{ID: 4, Kind: CommandAck, Ack: Ack{Token: 1, Payload: []byte("a")}},
		{ID: 5, Kind: CommandAck, Ack: Ack{Token: 2}},
	}
	for i := range cmds {
		in := &cmds[i]
		frame := AppendFramedCommandBebop(nil, in)
		if len(frame) != FramedCommandSizeBebop(in) {
			t.Fatalf("command %d size mismatch", i)
		}
		out, err := DecodeBebopCommands(frame, nil)
		if err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
		switch in.Kind {
		case CommandPublish:
			if out[0].Publish.RoomID != in.Publish.RoomID ||
				string(out[0].Publish.Room) != string(in.Publish.Room) ||
				out[0].Publish.Event.ID != in.Publish.Event.ID ||
				string(out[0].Publish.Event.Name) != string(in.Publish.Event.Name) ||
				string(out[0].Publish.Payload) != string(in.Publish.Payload) ||
				out[0].Publish.RequestID != in.Publish.RequestID {
				t.Fatalf("command %d publish = %+v", i, out[0].Publish)
			}
		case CommandSend:
			if out[0].Send.Event.ID != in.Send.Event.ID || out[0].Send.RequestID != in.Send.RequestID {
				t.Fatalf("command %d send = %+v", i, out[0].Send)
			}
		case CommandAck:
			if out[0].Ack.Token != in.Ack.Token || string(out[0].Ack.Payload) != string(in.Ack.Payload) {
				t.Fatalf("command %d ack = %+v", i, out[0].Ack)
			}
		}
	}

	pushes := []Push{
		{Kind: PushEvent, Event: EventPush{
			RoomID: 7, EventID: 1024, Payload: []byte("e"),
		}},
		{Kind: PushEvent, Event: EventPush{
			Room: []byte("chat"), EventID: 1024, Payload: []byte("e"), RequestID: 8, Offset: 9,
		}},
		{Kind: PushMessage, Message: Message{
			EventID: 1024, Payload: []byte("m"), RequestID: 10,
		}},
		{Kind: PushDisconnect, Disconnect: Disconnect{Code: 1000, Reason: []byte("bye")}},
		{Kind: PushRequest, Request: Request{Token: 1, EventID: 2, Payload: []byte("r")}},
	}
	for i := range pushes {
		in := &pushes[i]
		frame := AppendFramedPushBebop(nil, in)
		if len(frame) != FramedPushSizeBebop(in) {
			t.Fatalf("push %d size mismatch", i)
		}
		out, err := DecodeBebopReplies(frame, nil)
		if err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		if len(out) != 1 || out[0].Kind != ReplyPush || out[0].Push.Kind != in.Kind {
			t.Fatalf("push %d = %+v", i, out)
		}
	}
}

func TestBebopMalformedInput(t *testing.T) {
	if _, err := DecodeBebopCommands(nil, nil); err != ErrFraming {
		t.Fatalf("empty = %v", err)
	}
	if _, err := DecodeBebopCommands([]byte{0x80}, nil); err != ErrFraming {
		t.Fatalf("truncated varint = %v", err)
	}
	if _, err := DecodeBebopCommands([]byte{0x05, 0x01}, nil); err != ErrFraming {
		t.Fatalf("truncated body = %v", err)
	}
	// A valid frame prefix with a truncated command body.
	if _, err := DecodeBebopCommands([]byte{0x02, 0x01, 0x02}, nil); err != ErrWire {
		t.Fatalf("truncated command = %v", err)
	}
}

func TestBebopPayloadAliasesFrame(t *testing.T) {
	in := Command{Kind: CommandPublish, Publish: Publish{
		Room: []byte("r"), Event: EventRef{ID: 1}, Payload: []byte("hello"),
	}}
	frame := AppendFramedCommandBebop(nil, &in)
	out, err := DecodeBebopCommands(frame, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := out[0].Publish.Payload
	// RequestID is zero here and therefore absent, so the payload is last.
	frame[len(frame)-1] = 'X'
	if string(payload) != "hellX" {
		t.Fatalf("payload does not alias frame: %q", payload)
	}
}

func TestBebopDecodePublishDoesNotAllocate(t *testing.T) {
	in := Command{ID: 1, Kind: CommandPublish, Publish: Publish{
		Room: []byte("chat"), Event: EventRef{ID: 1024}, Payload: make([]byte, 64),
	}}
	frame := AppendFramedCommandBebop(nil, &in)
	var dst []Command
	dst, _ = DecodeBebopCommands(frame, dst)
	dst = dst[:0]
	if allocs := testing.AllocsPerRun(1000, func() {
		dst, _ = DecodeBebopCommands(frame, dst[:0])
	}); allocs != 0 {
		t.Fatalf("DecodeBebopCommands allocated %.2f times", allocs)
	}
}

func TestBebopEncodePushDoesNotAllocate(t *testing.T) {
	p := Push{Kind: PushEvent, Event: EventPush{
		Room: []byte("chat"), EventID: 1024, Payload: make([]byte, 64),
	}}
	buf := make([]byte, 0, FramedPushSizeBebop(&p))
	buf = AppendFramedPushBebop(buf, &p)
	if allocs := testing.AllocsPerRun(1000, func() {
		buf = AppendFramedPushBebop(buf[:0], &p)
	}); allocs != 0 {
		t.Fatalf("AppendFramedPushBebop allocated %.2f times", allocs)
	}
}
