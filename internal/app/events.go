//go:build linux

package app

import (
	"errors"
	"fmt"

	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/internal/fnv"
)

// EventHandler handles one inbound event by numeric ID. The payload is
// borrowed: it is valid only until the handler returns. This is the low-level
// API behind On; prefer On("name", handler).
type EventHandler = event.Handler[*Conn]

// HandlerFunc handles one named event. The payload is borrowed: it is valid
// only until the handler returns. Copy it to retain it.
type HandlerFunc func(c *Conn, data []byte)

// RequestHandler handles one named request and returns the acknowledgement
// payload. Returning an error sends an error acknowledgement.
type RequestHandler func(c *Conn, req []byte) ([]byte, error)

// ConnectHandler runs after the WebSocket upgrade and before any event. It
// may reject the connection with an error. It runs inline on the owning loop.
type ConnectHandler func(c *Conn) error

// DisconnectHandler runs exactly once when the connection leaves the open
// state. Rooms are already removed. It runs inline on the owning loop.
type DisconnectHandler func(c *Conn)

// EventMiddleware wraps an event handler. Registration order is execution
// order. A middleware runs inline on the owning loop and must not block.
type EventMiddleware func(next EventHandler) EventHandler

// ConnectMiddleware runs after the WebSocket upgrade completes and before the
// lifecycle OnConnect callback. Returning an error closes the connection with
// code 1008. It runs inline on the owning loop and must not block.
type ConnectMiddleware func(c *Conn) error

// Handler receives connection lifecycle callbacks. For the small API, prefer
// OnConnect/OnDisconnect; SetHandler remains for the low-level Message stream.
type Handler interface {
	// OnOpen is called after the WebSocket upgrade completes, before any
	// event is dispatched.
	OnOpen(c *Conn) error

	// OnMessage is called for each complete message whose event has no
	// registered EventHandler.
	OnMessage(c *Conn, m Message) error

	// OnClose is called exactly once when the connection leaves the Open
	// state. The Conn handle is already closed and Emit returns ErrClosed.
	OnClose(c *Conn, info CloseInfo)
}

// EventIDForName returns the stable FNV-1a 32-bit ID for an event name. The
// string API uses this ID on the wire; clients must use the same function.
func EventIDForName(name string) uint32 {
	h := fnv.Hash32(name)
	if h == 0 {
		return 1
	}
	return h
}

// registerBeforeStart is the shared guard for all registration APIs.
func (s *Server) registerBeforeStart(name string) error {
	if s == nil {
		return ErrClosed
	}
	if s.started.Load() {
		return fmt.Errorf("encryl: %s after ListenAndServe", name)
	}
	return nil
}

// On binds a named event to a handler. The name is hashed to the numeric wire
// ID with EventIDForName. Registration must happen before ListenAndServe.
//
//	srv.On("chat", func(c *encryl.Conn, data []byte) {
//	    c.To("general").Emit("chat", data)
//	})
func (s *Server) On(name string, h HandlerFunc) error {
	if err := s.registerBeforeStart("On"); err != nil {
		return err
	}
	if name == "" {
		return errors.New("encryl: event name must not be empty")
	}
	if h == nil {
		return errors.New("encryl: nil event handler")
	}
	id := EventIDForName(name)
	if err := s.RegisterEvent(name, id); err != nil {
		return err
	}
	return s.events.On(id, func(c *Conn, m Message) error {
		h(c, m.Payload)
		return nil
	})
}

// OnID binds a numeric event ID to a low-level handler. Prefer On. It exists
// for internal benchmarks and clients that already speak numeric IDs.
func (s *Server) OnID(eventID uint32, h EventHandler) error {
	if err := s.registerBeforeStart("OnID"); err != nil {
		return err
	}
	if h == nil {
		return errors.New("encryl: nil EventHandler")
	}
	return s.events.On(eventID, h)
}

// OnRequest binds a named request to a request handler. The response is
// sent as the acknowledgement payload; an error sends an error acknowledgement.
// Registration must happen before ListenAndServe.
func (s *Server) OnRequest(name string, h RequestHandler) error {
	if err := s.registerBeforeStart("OnRequest"); err != nil {
		return err
	}
	if name == "" {
		return errors.New("encryl: request name must not be empty")
	}
	if h == nil {
		return errors.New("encryl: nil request handler")
	}
	id := EventIDForName(name)
	if err := s.RegisterEvent(name, id); err != nil {
		return err
	}
	return s.events.On(id, func(c *Conn, m Message) error {
		resp, err := h(c, m.Payload)
		if err != nil {
			return err
		}
		return c.Reply(m, resp)
	})
}

// OnRequestID is the numeric low-level form of OnRequest.
func (s *Server) OnRequestID(eventID uint32, h RequestHandler) error {
	if err := s.registerBeforeStart("OnRequestID"); err != nil {
		return err
	}
	if h == nil {
		return errors.New("encryl: nil request handler")
	}
	return s.events.On(eventID, func(c *Conn, m Message) error {
		resp, err := h(c, m.Payload)
		if err != nil {
			return err
		}
		return c.Reply(m, resp)
	})
}

// OnConnect registers the connection handler. It may be called once, before
// ListenAndServe. It takes precedence over Handler.OnOpen.
func (s *Server) OnConnect(h ConnectHandler) error {
	if err := s.registerBeforeStart("OnConnect"); err != nil {
		return err
	}
	if h == nil {
		return errors.New("encryl: nil connect handler")
	}
	if s.connectHandler.Load() != nil {
		return errors.New("encryl: connect handler already registered")
	}
	s.connectHandler.Store(&h)
	return nil
}

// OnDisconnect registers the disconnect handler. It may be called once,
// before ListenAndServe. It takes precedence over Handler.OnClose.
func (s *Server) OnDisconnect(h DisconnectHandler) error {
	if err := s.registerBeforeStart("OnDisconnect"); err != nil {
		return err
	}
	if h == nil {
		return errors.New("encryl: nil disconnect handler")
	}
	if s.disconnectHandler.Load() != nil {
		return errors.New("encryl: disconnect handler already registered")
	}
	s.disconnectHandler.Store(&h)
	return nil
}

// RegisterEvent adds a string event name before the server starts. On and
// OnRequest call it automatically. It is useful for a Bebop client that
// publishes by name while no Go handler is bound.
func (s *Server) RegisterEvent(name string, id uint32) error {
	if err := s.registerBeforeStart("RegisterEvent"); err != nil {
		return err
	}
	return s.eventNames.Register(name, id)
}

// Use appends per-event middleware. It must be called before the first On.
func (s *Server) Use(mw EventMiddleware) error {
	if s == nil {
		return ErrClosed
	}
	if mw == nil {
		return errors.New("encryl: nil EventMiddleware")
	}
	return s.events.Use(func(next event.Handler[*Conn]) event.Handler[*Conn] {
		return mw(next)
	})
}

// UseConnect appends connection middleware. It runs after the upgrade and
// before OnConnect. A reject closes the connection with code 1008.
func (s *Server) UseConnect(mw ConnectMiddleware) error {
	if s == nil {
		return ErrClosed
	}
	if mw == nil {
		return errors.New("encryl: nil ConnectMiddleware")
	}
	if s.started.Load() {
		return errors.New("encryl: UseConnect after ListenAndServe")
	}
	s.mu.Lock()
	s.connectMW = append(s.connectMW, mw)
	s.mu.Unlock()
	return nil
}

// SetHandler registers low-level lifecycle callbacks. It must be called before
// ListenAndServe and may be called only once. OnConnect/OnDisconnect take
// precedence for the callbacks they provide.
func (s *Server) SetHandler(h Handler) error {
	if s == nil {
		return ErrClosed
	}
	if h == nil {
		return errors.New("encryl: nil Handler")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifecyclePtr.Load() != nil {
		return errors.New("encryl: Handler already registered")
	}
	s.lifecyclePtr.Store(&h)
	return nil
}
