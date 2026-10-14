//go:build linux

package app

import (
	"errors"
	"time"
)

// Topic is a precomputed event name and ID. Creating a Topic hashes the name
// once; Topic methods never hash. Use it in handlers that emit at high rates:
//
//	chat := srv.Topic("chat")
//	chat.On(func(c *encryl.Conn, data []byte) {
//	    _ = chat.To(c, "general").Emit(data)
//	})
//
// Server.On("chat", handler) is the one-line convenience; its handler may call
// Conn.Emit("chat", data), which hashes once per emit. Topic exists so a
// million-message-per-second handler can avoid even that.
type Topic struct {
	srv  *Server
	name string
	id   uint32
}

// Topic returns a precomputed topic for name. It does not register anything
// until On, OnRequest, or Register is called. A nil Topic is returned for an
// empty name or a nil server.
func (s *Server) Topic(name string) *Topic {
	if s == nil || name == "" {
		return nil
	}
	return &Topic{srv: s, name: name, id: EventIDForName(name)}
}

// Name returns the event name.
func (t *Topic) Name() string {
	if t == nil {
		return ""
	}
	return t.name
}

// ID returns the numeric wire ID.
func (t *Topic) ID() uint32 {
	if t == nil {
		return 0
	}
	return t.id
}

// Register adds the name to the server's name registry. On and OnRequest do it
// automatically. It is needed for a protobuf client that publishes by name
// while no Go handler is bound.
func (t *Topic) Register() error {
	if t == nil || t.srv == nil {
		return ErrClosed
	}
	return t.srv.RegisterEvent(t.name, t.id)
}

// On binds a named handler without hashing again.
func (t *Topic) On(h HandlerFunc) error {
	if t == nil || t.srv == nil {
		return ErrClosed
	}
	if h == nil {
		return errors.New("encryl: nil event handler")
	}
	if t.srv.started.Load() {
		return errors.New("encryl: Topic.On after ListenAndServe")
	}
	if err := t.srv.RegisterEvent(t.name, t.id); err != nil {
		return err
	}
	return t.srv.events.On(t.id, func(c *Conn, m Message) error {
		h(c, m.Payload)
		return nil
	})
}

// OnRequest binds a named request handler without hashing again.
func (t *Topic) OnRequest(h RequestHandler) error {
	if t == nil || t.srv == nil {
		return ErrClosed
	}
	if h == nil {
		return errors.New("encryl: nil request handler")
	}
	if t.srv.started.Load() {
		return errors.New("encryl: Topic.OnRequest after ListenAndServe")
	}
	if err := t.srv.RegisterEvent(t.name, t.id); err != nil {
		return err
	}
	return t.srv.events.On(t.id, func(c *Conn, m Message) error {
		resp, err := h(c, m.Payload)
		if err != nil {
			return err
		}
		return c.Reply(m, resp)
	})
}

// Emit sends this topic to c without hashing.
func (t *Topic) Emit(c *Conn, data []byte) error {
	if t == nil {
		return ErrClosed
	}
	return c.EmitID(t.id, data)
}

// Ask sends this topic to c and waits for an acknowledgement without hashing.
func (t *Topic) Ask(c *Conn, data []byte, timeout time.Duration, cb func(Ack)) error {
	if t == nil {
		return ErrClosed
	}
	return c.EmitWithAckID(t.id, data, timeout, cb)
}

// To targets a room from c, excluding c. It does not hash on Emit.
func (t *Topic) To(c *Conn, room string) TopicTarget {
	if t == nil {
		return TopicTarget{}
	}
	return TopicTarget{topic: t, emitter: c.To(room)}
}

// ToRoom targets a room from c, including c. It does not hash on Emit.
func (t *Topic) ToRoom(c *Conn, room string) TopicTarget {
	if t == nil {
		return TopicTarget{}
	}
	return TopicTarget{topic: t, emitter: c.ToRoom(room)}
}

// ToConns targets connection IDs. It does not hash on Emit.
func (t *Topic) ToConns(ids ...ConnID) TopicTarget {
	if t == nil || t.srv == nil {
		return TopicTarget{}
	}
	return TopicTarget{topic: t, emitter: t.srv.ToConns(ids...)}
}

// TopicTarget is a precomputed topic plus an emitter. Obtain one from
// Topic.To, Topic.ToRoom, or Topic.ToConns.
type TopicTarget struct {
	topic   *Topic
	emitter Emitter
}

// Except removes connection IDs.
func (t TopicTarget) Except(ids ...ConnID) TopicTarget {
	t.emitter = t.emitter.Except(ids...)
	return t
}

// Volatile marks the send as best-effort.
func (t TopicTarget) Volatile() TopicTarget {
	t.emitter = t.emitter.Volatile()
	return t
}

// Emit sends the precomputed topic without hashing.
func (t TopicTarget) Emit(data []byte) error {
	if t.topic == nil {
		return ErrClosed
	}
	return t.emitter.EmitID(t.topic.id, data)
}
