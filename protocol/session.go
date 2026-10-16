package protocol

import "sync"

// Session is the per-connection state of one protocol connection. It is owned
// by the connection's loop. The native codec does not use a Session.
type Session struct {
	cmds []Command

	connected bool
	nextRoom  uint32
	rooms     map[uint32]string
	ids       map[string]uint32
}

var sessionPool = sync.Pool{New: func() any {
	return &Session{
		cmds:  make([]Command, 0, 8),
		rooms: make(map[uint32]string, 8),
		ids:   make(map[string]uint32, 8),
	}
}}

// AcquireSession returns a reset session from the pool.
func AcquireSession() *Session {
	s := sessionPool.Get().(*Session)
	s.Reset()
	return s
}

// ReleaseSession returns a session to the pool.
func ReleaseSession(s *Session) {
	if s == nil {
		return
	}
	s.Reset()
	sessionPool.Put(s)
}

// Reset clears the session while keeping its maps and command scratch.
func (s *Session) Reset() {
	s.connected = false
	s.nextRoom = 0
	clear(s.rooms)
	clear(s.ids)
	s.cmds = s.cmds[:0]
}

// Decode splits one frame into commands. The returned payloads alias frame.
func (s *Session) Decode(frame []byte) ([]Command, error) {
	cmds, err := DecodeCommands(frame, s.cmds)
	s.cmds = cmds
	return cmds, err
}

// Connected reports whether the connect command succeeded.
func (s *Session) Connected() bool { return s.connected }

// SetConnected records a successful connect.
func (s *Session) SetConnected(v bool) { s.connected = v }

// AddRoom assigns or returns the handle for a room name. The name is copied
// into the session; this is a control-path allocation.
func (s *Session) AddRoom(name string) uint32 {
	if id, ok := s.ids[name]; ok {
		return id
	}
	s.nextRoom++
	id := s.nextRoom
	s.rooms[id] = name
	s.ids[name] = id
	return id
}

// RoomName resolves a room handle.
func (s *Session) RoomName(id uint32) (string, bool) {
	name, ok := s.rooms[id]
	return name, ok
}

// RemoveRoom drops a room handle.
func (s *Session) RemoveRoom(id uint32) {
	name, ok := s.rooms[id]
	if !ok {
		return
	}
	delete(s.rooms, id)
	delete(s.ids, name)
}
