package protocol

import (
	"bytes"
	"testing"
)

func TestCommandPublishRoundTrip(t *testing.T) {
	in := Command{
		ID:   7,
		Kind: CommandPublish,
		Publish: Publish{
			Room:      []byte("chat"),
			Event:     EventRef{ID: 1024},
			Payload:   []byte("hello"),
			RequestID: 99,
		},
	}
	frame := AppendFramedCommand(nil, &in)
	cmds, err := DecodeCommands(frame, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 {
		t.Fatalf("commands = %d", len(cmds))
	}
	got := cmds[0]
	if got.ID != 7 || got.Kind != CommandPublish {
		t.Fatalf("command = %+v", got)
	}
	if string(got.Publish.Room) != "chat" || got.Publish.Event.ID != 1024 ||
		string(got.Publish.Payload) != "hello" || got.Publish.RequestID != 99 {
		t.Fatalf("publish = %+v", got.Publish)
	}
}

func TestCommandConnectRoundTrip(t *testing.T) {
	in := Command{
		ID:   1,
		Kind: CommandConnect,
		Connect: Connect{
			Token:         []byte("token"),
			Client:        []byte("go"),
			Version:       []byte("1"),
			Subscriptions: [][]byte{[]byte("a"), []byte("b")},
		},
	}
	cmds, err := DecodeCommands(AppendFramedCommand(nil, &in), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := cmds[0].Connect
	if string(c.Token) != "token" || string(c.Client) != "go" || len(c.Subscriptions) != 2 {
		t.Fatalf("connect = %+v", c)
	}
}

func TestCommandEventNameAndAck(t *testing.T) {
	in := Command{ID: 2, Kind: CommandSend, Send: Send{
		Event: EventRef{Name: []byte("chat.message")}, Payload: []byte("x"),
	}}
	cmds, err := DecodeCommands(AppendFramedCommand(nil, &in), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(cmds[0].Send.Event.Name) != "chat.message" {
		t.Fatalf("send = %+v", cmds[0].Send)
	}

	ack := Command{Kind: CommandAck, Ack: Ack{Token: 42, Payload: []byte("ok")}}
	cmds, err = DecodeCommands(AppendFramedCommand(nil, &ack), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmds[0].Ack.Token != 42 || string(cmds[0].Ack.Payload) != "ok" {
		t.Fatalf("ack = %+v", cmds[0].Ack)
	}
}

func TestReplyAndPushRoundTrip(t *testing.T) {
	r := Reply{ID: 5, Kind: ReplyConnect, Connect: ConnectResult{
		ClientID: []byte("c1"), PingSeconds: 25, Pong: true,
		Subscriptions: []SubscribeResult{{RoomID: 1, EventID: 1024}},
	}}
	replies, err := DecodeReplies(AppendFramedReply(nil, &r), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 1 || replies[0].ID != 5 || !replies[0].Connect.Pong {
		t.Fatalf("replies = %+v", replies)
	}
	if len(replies[0].Connect.Subscriptions) != 1 || replies[0].Connect.Subscriptions[0].RoomID != 1 {
		t.Fatalf("subscriptions = %+v", replies[0].Connect.Subscriptions)
	}

	p := Push{Kind: PushEvent, Event: EventPush{
		Room: []byte("chat"), EventID: 1024, Payload: []byte("hi"), RequestID: 9,
	}}
	frame := AppendFramedPush(nil, &p)
	replies, err = DecodeReplies(frame, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 1 || replies[0].ID != 0 || replies[0].Kind != ReplyPush {
		t.Fatalf("push reply = %+v", replies)
	}
	e := replies[0].Push.Event
	if string(e.Room) != "chat" || e.EventID != 1024 || string(e.Payload) != "hi" || e.RequestID != 9 {
		t.Fatalf("event push = %+v", e)
	}

	m := Push{Kind: PushMessage, Message: Message{EventID: 77, Payload: []byte("self")}}
	replies, err = DecodeReplies(AppendFramedPush(nil, &m), nil)
	if err != nil {
		t.Fatal(err)
	}
	if replies[0].Push.Message.EventID != 77 || string(replies[0].Push.Message.Payload) != "self" {
		t.Fatalf("message push = %+v", replies[0].Push.Message)
	}
}

func TestBatchFrame(t *testing.T) {
	a := Command{ID: 1, Kind: CommandSubscribe, Subscribe: Subscribe{Room: []byte("a")}}
	b := Command{ID: 2, Kind: CommandSubscribe, Subscribe: Subscribe{Room: []byte("b")}}
	frame := AppendFramedCommand(nil, &a)
	frame = AppendFramedCommand(frame, &b)
	cmds, err := DecodeCommands(frame, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 || cmds[0].ID != 1 || cmds[1].ID != 2 {
		t.Fatalf("cmds = %+v", cmds)
	}
}

func TestPayloadAliasesFrame(t *testing.T) {
	in := Command{Kind: CommandPublish, Publish: Publish{
		Room: []byte("r"), Event: EventRef{ID: 1}, Payload: []byte("hello"),
	}}
	frame := AppendFramedCommand(nil, &in)
	cmds, err := DecodeCommands(frame, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := cmds[0].Publish.Payload
	if len(payload) == 0 || !bytes.Equal(payload, []byte("hello")) {
		t.Fatalf("payload = %q", payload)
	}
	frame[len(frame)-1] = 'X'
	if string(payload) != "hellX" {
		t.Fatalf("payload does not alias frame: %q", payload)
	}
}

func TestMalformedFraming(t *testing.T) {
	if _, err := DecodeCommands([]byte{0x05, 0x01}, nil); err == nil {
		t.Fatal("truncated frame accepted")
	}
	if _, err := DecodeCommands([]byte{0x80}, nil); err == nil {
		t.Fatal("truncated varint accepted")
	}
}

func TestDecodePublishDoesNotAllocate(t *testing.T) {
	in := Command{ID: 1, Kind: CommandPublish, Publish: Publish{
		Room: []byte("chat"), Event: EventRef{ID: 1024}, Payload: make([]byte, 64),
	}}
	frame := AppendFramedCommand(nil, &in)
	var dst []Command
	dst, _ = DecodeCommands(frame, dst)
	dst = dst[:0]
	allocs := testing.AllocsPerRun(1000, func() {
		dst, _ = DecodeCommands(frame, dst[:0])
	})
	if allocs != 0 {
		t.Fatalf("DecodeCommands allocated %.2f times", allocs)
	}
}

func TestEncodePushDoesNotAllocate(t *testing.T) {
	p := Push{Kind: PushEvent, Event: EventPush{
		Room: []byte("chat"), EventID: 1024, Payload: make([]byte, 64),
	}}
	buf := make([]byte, 0, 256)
	buf = AppendFramedPush(buf, &p)
	allocs := testing.AllocsPerRun(1000, func() {
		buf = AppendFramedPush(buf[:0], &p)
	})
	if allocs != 0 {
		t.Fatalf("AppendFramedPush allocated %.2f times", allocs)
	}
}

func TestEventRegistry(t *testing.T) {
	r := NewEventRegistry()
	if err := r.Register("chat.message", 1024); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("chat.message", 1024); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("chat.message", 1025); err == nil {
		t.Fatal("duplicate name with a new ID accepted")
	}
	if id, ok := r.ID("chat.message"); !ok || id != 1024 {
		t.Fatalf("ID = %d %v", id, ok)
	}
	if id, ok := r.IDBytes([]byte("chat.message")); !ok || id != 1024 {
		t.Fatalf("IDBytes = %d %v", id, ok)
	}
	if err := r.Register("", 1); err == nil {
		t.Fatal("empty name accepted")
	}
}

func TestGoldenSendFrame(t *testing.T) {
	cmd := Command{
		ID:   1,
		Kind: CommandSend,
		Send: Send{Event: EventRef{ID: 1024}, Payload: []byte("hi")},
	}
	want := []byte{0x0d, 0x08, 0x01, 0x32, 0x09, 0x0a, 0x03, 0x08, 0x80, 0x08, 0x12, 0x02, 0x68, 0x69}
	if got := AppendFramedCommand(nil, &cmd); !bytes.Equal(got, want) {
		t.Fatalf("frame = % x, want % x", got, want)
	}
}

func TestEventRegistryFreeze(t *testing.T) {
	r := NewEventRegistry()
	if err := r.Register("chat", 1024); err != nil {
		t.Fatal(err)
	}
	r.Freeze()
	if err := r.Register("later", 1025); err == nil {
		t.Fatal("Register after Freeze succeeded")
	}
	if id, ok := r.ID("chat"); !ok || id != 1024 {
		t.Fatalf("ID after Freeze = %d %v", id, ok)
	}
	if _, ok := r.IDBytes([]byte("missing")); ok {
		t.Fatal("missing name resolved")
	}
}
