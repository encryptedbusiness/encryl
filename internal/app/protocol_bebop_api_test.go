//go:build linux

package app

import (
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/protocol"
	"github.com/gorilla/websocket"
)

func testBebopConfig() Config {
	cfg := testServerConfig()
	cfg.EventNames = map[string]uint32{
		"chat.message": 1024,
		"chat.reply":   2048,
	}
	cfg.ChannelEvents = map[string]uint32{"room": 1024}
	return cfg
}

func dialBebop(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{
		HandshakeTimeout: 2 * time.Second,
		Subprotocols:     []string{protocol.SubprotocolBebopV1},
	}
	c, resp, err := d.Dial(url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if got := c.Subprotocol(); got != protocol.SubprotocolBebopV1 {
		_ = c.Close()
		t.Fatalf("subprotocol = %q", got)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func writeBebop(t *testing.T, c *websocket.Conn, cmds ...protocol.Command) {
	t.Helper()
	var frame []byte
	for i := range cmds {
		frame = protocol.AppendFramedCommandBebop(frame, &cmds[i])
	}
	if err := c.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
}

func readBebop(t *testing.T, c *websocket.Conn) []protocol.Reply {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	replies, err := protocol.DecodeBebopReplies(data, nil)
	if err != nil {
		t.Fatalf("DecodeBebopReplies: %v", err)
	}
	return replies
}

func connectBebop(t *testing.T, c *websocket.Conn) protocol.ConnectResult {
	t.Helper()
	writeBebop(t, c, protocol.Command{
		ID:      1,
		Kind:    protocol.CommandConnect,
		Connect: protocol.Connect{Client: []byte("test"), Version: []byte("1")},
	})
	for _, r := range readBebop(t, c) {
		if r.ID == 1 && r.Kind == protocol.ReplyConnect {
			if r.Error != nil {
				t.Fatalf("connect error = %+v", r.Error)
			}
			return r.Connect
		}
	}
	t.Fatal("connect reply not received")
	return protocol.ConnectResult{}
}

func subscribeBebop(t *testing.T, c *websocket.Conn, id uint32, room string) protocol.SubscribeResult {
	t.Helper()
	writeBebop(t, c, protocol.Command{
		ID:        id,
		Kind:      protocol.CommandSubscribe,
		Subscribe: protocol.Subscribe{Room: []byte(room)},
	})
	for _, r := range readBebop(t, c) {
		if r.ID == id && r.Kind == protocol.ReplySubscribe {
			if r.Error != nil {
				t.Fatalf("subscribe error = %+v", r.Error)
			}
			return r.Subscribe
		}
	}
	t.Fatal("subscribe reply not received")
	return protocol.SubscribeResult{}
}

func TestBebopConnectSubscribePublish(t *testing.T) {
	srv, err := New(testBebopConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened := openedConns(srv)
	url := startTestServer(t, srv)

	a := dialBebop(t, url)
	<-opened
	b := dialBebop(t, url)
	<-opened

	connectBebop(t, a)
	connectBebop(t, b)
	sub := subscribeBebop(t, a, 2, "room")
	if sub.RoomID == 0 || sub.EventID != 1024 {
		t.Fatalf("subscribe = %+v", sub)
	}
	subscribeBebop(t, b, 2, "room")

	writeBebop(t, a, protocol.Command{
		ID:   3,
		Kind: protocol.CommandPublish,
		Publish: protocol.Publish{
			RoomID:  sub.RoomID,
			Payload: []byte("hello"),
		},
	})

	replies := readBebop(t, b)
	if len(replies) != 1 || replies[0].Kind != protocol.ReplyPush || replies[0].Push.Kind != protocol.PushEvent {
		t.Fatalf("b replies = %+v", replies)
	}
	push := replies[0].Push.Event
	if string(push.Room) != "room" || push.EventID != 1024 || string(push.Payload) != "hello" {
		t.Fatalf("event push = %+v", push)
	}

	seenPush, seenReply := false, false
	for i := 0; i < 2; i++ {
		for _, r := range readBebop(t, a) {
			if r.Kind == protocol.ReplyPush && r.Push.Kind == protocol.PushEvent {
				seenPush = true
			}
			if r.ID == 3 && r.Kind == protocol.ReplyPublish {
				seenReply = true
			}
		}
	}
	if !seenPush || !seenReply {
		t.Fatalf("publisher saw push=%v reply=%v", seenPush, seenReply)
	}
}

func TestBebopSelfRoomAndAck(t *testing.T) {
	srv, err := New(testBebopConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened := openedConns(srv)
	url := startTestServer(t, srv)
	c := dialBebop(t, url)
	sc := <-opened
	connectBebop(t, c)

	if err := sc.EmitID(2048, []byte("self")); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	replies := readBebop(t, c)
	if len(replies) != 1 || replies[0].Push.Kind != protocol.PushMessage {
		t.Fatalf("self replies = %+v", replies)
	}
	if replies[0].Push.Message.EventID != 2048 || string(replies[0].Push.Message.Payload) != "self" {
		t.Fatalf("self push = %+v", replies[0].Push.Message)
	}

	acks := make(chan Ack, 1)
	if err := sc.EmitWithAckID(2048, []byte("question"), time.Second, func(a Ack) {
		acks <- a
	}); err != nil {
		t.Fatalf("EmitWithAck: %v", err)
	}
	replies = readBebop(t, c)
	if len(replies) != 1 || replies[0].Push.Kind != protocol.PushRequest {
		t.Fatalf("request push = %+v", replies)
	}
	req := replies[0].Push.Request
	if req.Token == 0 || req.EventID != 2048 {
		t.Fatalf("request = %+v", req)
	}
	writeBebop(t, c, protocol.Command{
		Kind: protocol.CommandAck,
		Ack:  protocol.Ack{Token: req.Token, Payload: []byte("answer")},
	})
	select {
	case a := <-acks:
		if a.Err != nil || string(a.Payload) != "answer" {
			t.Fatalf("ack = %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ack callback did not run")
	}
}

func TestBebopHandlerReply(t *testing.T) {
	srv, err := New(testBebopConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(2048, func(c *Conn, m Message) error {
		return c.Reply(m, []byte("pong"))
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	c := dialBebop(t, url)
	connectBebop(t, c)

	writeBebop(t, c, protocol.Command{
		ID:   4,
		Kind: protocol.CommandPublish,
		Publish: protocol.Publish{
			Event:   protocol.EventRef{ID: 2048},
			Payload: []byte("ping"),
		},
	})
	for _, r := range readBebop(t, c) {
		if r.ID == 4 && r.Kind == protocol.ReplyAck {
			if string(r.Ack.Payload) != "pong" {
				t.Fatalf("ack = %+v", r.Ack)
			}
			return
		}
	}
	t.Fatal("reply not received")
}

func TestBebopAndNativeRoomFanout(t *testing.T) {
	srv, err := New(testBebopConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.UseConnect(func(c *Conn) error { return c.Join("room") }); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)
	native := dialWS(t, url)
	bb := dialBebop(t, url)

	deadline := time.Now().Add(2 * time.Second)
	for srv.Stats().Memberships < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := srv.Stats().Memberships; got != 2 {
		t.Fatalf("memberships = %d", got)
	}

	if err := srv.To("room").EmitID(1024, []byte("fanout")); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	e := readEnvelope(t, native)
	if e.Kind != event.KindEvent || e.Event != 1024 || string(e.Payload) != "fanout" {
		t.Fatalf("native event = %+v", e)
	}

	for _, r := range readBebop(t, bb) {
		if r.Push.Kind != protocol.PushEvent {
			continue
		}
		push := r.Push.Event
		if string(push.Room) != "room" || push.EventID != 1024 || string(push.Payload) != "fanout" {
			t.Fatalf("bebop push = %+v", push)
		}
		return
	}
	t.Fatal("bebop client did not receive the room push")
}

func TestBebopBroadcastIsEncodingFiltered(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	native := dialWS(t, url)
	bb := dialBebop(t, url)

	deadline := time.Now().Add(2 * time.Second)
	for srv.Stats().ConnectionsOpen < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := srv.Stats().ConnectionsOpen; got < 2 {
		t.Fatalf("connections open = %d", got)
	}

	if err := srv.EmitID(1024, []byte("all")); err != nil {
		t.Fatalf("EmitID: %v", err)
	}

	e := readEnvelope(t, native)
	if e.Event != 1024 || string(e.Payload) != "all" {
		t.Fatalf("native event = %+v", e)
	}

	bbReplies := readBebop(t, bb)
	if len(bbReplies) != 1 || bbReplies[0].Push.Kind != protocol.PushEvent ||
		string(bbReplies[0].Push.Event.Payload) != "all" {
		t.Fatalf("bebop replies = %+v", bbReplies)
	}
}
