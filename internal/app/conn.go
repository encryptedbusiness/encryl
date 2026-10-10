//go:build linux

package app

import (
	"errors"
	"net"
	"strconv"
	"sync/atomic"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/protocol"
	"github.com/encryptedbusiness/encryl/reactor"
	"github.com/encryptedbusiness/encryl/room"
)

// Message is one decoded application message.
type Message = event.Message

// Ack is the outcome delivered to an EmitWithAck callback.
type Ack = event.Ack

// ConnID identifies a connection across its lifetime. The pair is what makes
// stale handles safe: a slot may be reused, but the generation differs, and
// every method validates the pair before touching state.
type ConnID struct {
	Slot uint32
	Gen  uint32
}

// CloseInfo describes why a connection ended.
type CloseInfo struct {
	// Code is the WebSocket close status code. Zero means the connection was
	// aborted without a close frame.
	Code uint16
	// Reason is the UTF-8 close reason supplied by the peer or the server.
	Reason string
	// Remote reports whether the peer initiated the close.
	Remote bool
}

// Conn is an opaque connection handle. The zero value is invalid; handlers
// receive a valid handle.
//
// A Conn is safe to hold across goroutines and across the connection's death:
// calls after close return ErrClosed rather than touching reused memory.
type Conn struct {
	srv        *Server
	loop       uint16
	slot       uint32
	gen        uint32
	acct       *connection.Accounting
	peerIP     [16]byte
	peerPort   uint16
	peerFamily uint8
	peerSet    bool

	closed   atomic.Bool
	encoding protocol.Encoding
	session  *protocol.Session
	userData atomic.Pointer[any]

	// replied is loop-local: set by Reply during a handler call and read
	// immediately after it returns to decide whether to auto-ACK.
	replied bool
}

// ID returns the connection identifier. It is stable for the connection's
// lifetime and never reused without a generation bump.
func (c *Conn) ID() ConnID {
	if c == nil || c.srv == nil {
		return ConnID{}
	}
	return ConnID{Slot: c.srv.globalSlot(int(c.loop), c.slot), Gen: c.gen}
}

// To returns an Emitter for the named rooms. It excludes the caller, matching
// socket.to(room). Chain Emit to send.
func (c *Conn) To(room string) Emitter {
	if c == nil || c.srv == nil {
		return Emitter{}
	}
	return Emitter{srv: c.srv, hasSelf: true, selfRef: c.ref(), hasRoom: true, room: room}
}

// ToRoom targets a room including this connection.
func (c *Conn) ToRoom(room string) Emitter {
	if c == nil || c.srv == nil {
		return Emitter{}
	}
	return Emitter{srv: c.srv, hasRoom: true, room: room}
}

// SetUserData stores one application value on the connection. The slot is a
// single atomic pointer, not a map. Pass a struct for multiple values.
func (c *Conn) SetUserData(v any) {
	if c == nil {
		return
	}
	c.userData.Store(&v)
}

// UserData returns the value stored by SetUserData.
func (c *Conn) UserData() any {
	if c == nil {
		return nil
	}
	p := c.userData.Load()
	if p == nil {
		return nil
	}
	return *p
}

// Rooms iterates the connection's room memberships. The Self-Room is included
// first. Returning false stops the iteration.
func (c *Conn) Rooms(fn func(name string) bool) {
	if c == nil || c.srv == nil || c.closed.Load() {
		return
	}
	if fn == nil {
		return
	}
	id := c.ID()
	var buf [20]byte
	if !fn(string(strconv.AppendUint(buf[:0], uint64(id.Slot), 10))) {
		return
	}
	c.srv.rooms.RoomNames(c.ref(), fn)
}

// Join adds the connection to a room. Joining a room twice is not an error.
func (c *Conn) Join(name string) error {
	if c == nil || c.srv == nil || c.closed.Load() {
		return ErrClosed
	}
	if err := c.srv.rooms.Join(name, c.ref()); err != nil {
		if errors.Is(err, room.ErrLimit) {
			return ErrRoomLimit
		}
		return err
	}
	if c.closed.Load() {
		c.srv.rooms.Leave(name, c.ref())
		return ErrClosed
	}
	return nil
}

// Leave removes the connection from a room. Leaving a room the connection is
// not in is not an error.
func (c *Conn) Leave(name string) error {
	if c == nil || c.srv == nil {
		return ErrClosed
	}
	c.srv.rooms.Leave(name, c.ref())
	return nil
}

// Close starts the closing handshake. It is idempotent. The close frame is
// sent with code and reason, then the connection drains for CloseTimeout
// before the descriptor is closed.
func (c *Conn) Close(code uint16, reason string) error {
	if c == nil || c.srv == nil {
		return ErrClosed
	}
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	var reasonBytes []byte
	if reason != "" {
		reasonBytes = []byte(reason)
	}
	return c.srv.enqueue(int(c.loop), reactor.Command{
		Kind: reactor.CmdClose,
		Slot: c.slot,
		Gen:  c.gen,
		A:    uint64(code),
		Data: reasonBytes,
	})
}

// RemoteAddr returns the peer address, or nil after the connection closes.
func (c *Conn) RemoteAddr() net.Addr {
	if c == nil || c.srv == nil || c.closed.Load() || !c.peerSet {
		return nil
	}
	n := 16
	if c.peerFamily == 4 {
		n = 4
	}
	ip := make(net.IP, n)
	copy(ip, c.peerIP[:n])
	return &net.TCPAddr{IP: ip, Port: int(c.peerPort)}
}

func (c *Conn) ref() connection.Ref {
	return connection.Ref{Loop: c.loop, Slot: c.slot, Gen: c.gen}
}
