//go:build linux

package app

import (
	"errors"
	"testing"
)

func TestTopicRegistration(t *testing.T) {
	srv := newRegistrationServer()
	topic := srv.Topic("chat")
	if topic == nil || topic.Name() != "chat" || topic.ID() != EventIDForName("chat") {
		t.Fatalf("topic = %+v", topic)
	}
	if srv.Topic("") != nil {
		t.Fatal("empty topic accepted")
	}
	var nilTopic *Topic
	if nilTopic.Name() != "" || nilTopic.ID() != 0 {
		t.Fatal("nil topic returned a value")
	}
	if err := nilTopic.Register(); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil Register = %v", err)
	}
	if err := nilTopic.Emit(nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("nil Emit = %v", err)
	}
	if err := (TopicTarget{}).Emit(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("zero TopicTarget = %v", err)
	}

	if err := topic.On(func(c *Conn, data []byte) {}); err != nil {
		t.Fatal(err)
	}
	if err := topic.On(func(c *Conn, data []byte) {}); err == nil {
		t.Fatal("duplicate topic handler accepted")
	}
	if err := topic.On(nil); err == nil {
		t.Fatal("nil topic handler accepted")
	}
	if err := srv.OnRequest("chat", func(c *Conn, req []byte) ([]byte, error) { return nil, nil }); err == nil {
		t.Fatal("topic event/request collision accepted")
	}
	rpc := srv.Topic("rpc")
	if err := rpc.OnRequest(func(c *Conn, req []byte) ([]byte, error) { return req, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestTopicAfterStart(t *testing.T) {
	srv := newRegistrationServer()
	srv.started.Store(true)
	topic := srv.Topic("chat")
	if err := topic.On(func(c *Conn, data []byte) {}); err == nil {
		t.Fatal("Topic.On accepted after start")
	}
	if err := topic.OnRequest(func(c *Conn, req []byte) ([]byte, error) { return nil, nil }); err == nil {
		t.Fatal("Topic.OnRequest accepted after start")
	}
}
