//go:build linux

package app

import (
	"sync"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/protocol"
	"github.com/encryptedbusiness/encryl/reactor"
	"github.com/encryptedbusiness/encryl/room"
)

var refScratchPool = sync.Pool{New: func() any {
	s := make([]connection.Ref, 0, 256)
	return &s
}}

// Emitter selects send targets and sends one encoded frame to all of them. The
// zero value is invalid; obtain one from Server.To, Server.ToConns, Conn.To, or
// Conn.Broadcast. Methods return a modified copy.
type Emitter struct {
	srv       *Server
	selfRef   connection.Ref
	hasSelf   bool
	room      string
	hasRoom   bool
	moreRooms []string
	ids       []ConnID
	except    []ConnID
	all       bool
	volatile  bool
}

// To adds a room to the target set. The first room uses the single-room fast
// path; chaining a second room allocates the multi-room list once.
func (e Emitter) To(room string) Emitter {
	if !e.hasRoom {
		e.room = room
		e.hasRoom = true
		return e
	}
	e.moreRooms = append(e.moreRooms, room)
	return e
}

// ToConns adds connection IDs to the target set.
func (e Emitter) ToConns(ids ...ConnID) Emitter {
	next := make([]ConnID, 0, len(e.ids)+len(ids))
	next = append(next, e.ids...)
	next = append(next, ids...)
	e.ids = next
	return e
}

// Except removes connection IDs from the target set.
func (e Emitter) Except(ids ...ConnID) Emitter {
	next := make([]ConnID, 0, len(e.except)+len(ids))
	next = append(next, e.except...)
	next = append(next, ids...)
	e.except = next
	return e
}

// Volatile marks the send as best-effort: it is dropped instead of queued when
// the target connection already has a frame in flight or queued.
func (e Emitter) Volatile() Emitter {
	e.volatile = true
	return e
}

// Emit encodes one frame per encoding and sends it to every selected target.
func (e Emitter) Emit(event string, payload []byte) error {
	return e.EmitID(EventIDForName(event), payload)
}

// EmitID is the numeric low-level form of Emit.
func (e Emitter) EmitID(eventID uint32, payload []byte) error {
	if e.srv == nil {
		return ErrClosed
	}
	if int64(len(payload)) > e.srv.cfg.MaxMessageSize-event.HeaderSize {
		return ErrPayloadTooLarge
	}
	return e.dispatch(eventID, payload, 0)
}

func (e Emitter) dispatch(eventID uint32, payload []byte, requestID uint64) error {
	s := e.srv
	if s == nil {
		return ErrClosed
	}
	st := s.state.Load()
	if st == nil {
		return ErrClosed
	}

	var except []connection.Ref
	if len(e.except) > 0 {
		except = make([]connection.Ref, 0, len(e.except))
		for _, id := range e.except {
			if ref, ok := room.SelfRef(connection.ID{Slot: id.Slot, Gen: id.Gen}, st.perLoop); ok {
				except = append(except, ref)
			}
		}
	}

	if e.all {
		return e.dispatchAll(st, eventID, payload, requestID, except)
	}

	var firstErr error
	if len(e.ids) > 0 {
		if err := e.dispatchIDs(st, except, eventID, payload, requestID); err != nil {
			firstErr = err
		}
	}
	if e.hasRoom {
		if err := e.dispatchRoom(st, e.room, except, eventID, payload, requestID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, name := range e.moreRooms {
		if err := e.dispatchRoom(st, name, except, eventID, payload, requestID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (e Emitter) dispatchAll(st *serverState, eventID uint32, payload []byte, requestID uint64, except []connection.Ref) error {
	s := e.srv
	var native, proto *connection.Frame
	if s.connCounts[0].Load() > 0 {
		native = e.nativeFrame(eventID, payload, requestID)
	}
	if s.connCounts[1].Load() > 0 {
		proto = e.protoEventFrame("", eventID, payload, requestID)
	}
	firstErr := error(nil)
	for _, l := range st.loops {
		if l == nil {
			continue
		}
		if native != nil {
			native.Retain()
			cmd := reactor.Command{
				Kind:          reactor.CmdBroadcast,
				Encoding:      0,
				Frame:         native,
				Except:        except,
				SelfExcept:    e.selfRef,
				HasSelfExcept: e.hasSelf,
			}
			if !l.Enqueue(cmd) {
				native.Release()
				s.backpressure.Add(1)
				firstErr = ErrBackpressure
			}
		}
		if proto != nil {
			proto.Retain()
			cmd := reactor.Command{
				Kind:          reactor.CmdBroadcast,
				Encoding:      1,
				Frame:         proto,
				Except:        except,
				SelfExcept:    e.selfRef,
				HasSelfExcept: e.hasSelf,
			}
			if !l.Enqueue(cmd) {
				proto.Release()
				s.backpressure.Add(1)
				firstErr = ErrBackpressure
			}
		}
	}
	if native != nil {
		native.Release()
	}
	if proto != nil {
		proto.Release()
	}
	return firstErr
}

func (e Emitter) dispatchIDs(st *serverState, except []connection.Ref, eventID uint32, payload []byte, requestID uint64) error {
	native := room.AcquirePlanner()
	proto := room.AcquirePlanner()
	for _, id := range e.ids {
		ref, ok := room.SelfRef(connection.ID{Slot: id.Slot, Gen: id.Gen}, st.perLoop)
		if !ok || e.excludes(except, ref) {
			continue
		}
		c := e.srv.connForRef(st, ref)
		if c == nil {
			continue
		}
		if c.encoding == protocol.EncodingProtobufV1 {
			proto.Add(ref)
		} else {
			native.Add(ref)
		}
	}
	var firstErr error
	if !native.Empty() {
		if err := e.srv.dispatchGroups(st, native.Groups(), e.nativeFrame(eventID, payload, requestID)); err != nil {
			firstErr = err
		}
	}
	if !proto.Empty() {
		if err := e.srv.dispatchGroups(st, proto.Groups(), e.protoMessageFrame(eventID, payload, requestID)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	native.Release()
	proto.Release()
	return firstErr
}

func (e Emitter) dispatchRoom(st *serverState, name string, except []connection.Ref, eventID uint32, payload []byte, requestID uint64) error {
	s := e.srv
	native := room.AcquirePlanner()
	proto := room.AcquirePlanner()

	p := refScratchPool.Get().(*[]connection.Ref)
	refs := (*p)[:0]
	refs = s.rooms.SnapshotInto(name, refs)
	for _, ref := range refs {
		if e.excludes(except, ref) {
			continue
		}
		c := s.connForRef(st, ref)
		if c == nil {
			continue
		}
		if c.encoding == protocol.EncodingProtobufV1 {
			proto.Add(ref)
		} else {
			native.Add(ref)
		}
	}
	*p = refs[:0]
	refScratchPool.Put(p)

	var firstErr error
	if !native.Empty() {
		if err := s.dispatchGroups(st, native.Groups(), e.nativeFrame(eventID, payload, requestID)); err != nil {
			firstErr = err
		}
	}
	if !proto.Empty() {
		if err := s.dispatchGroups(st, proto.Groups(), e.protoEventFrame(name, eventID, payload, requestID)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	native.Release()
	proto.Release()
	return firstErr
}

func (e Emitter) excludes(except []connection.Ref, ref connection.Ref) bool {
	if e.hasSelf && ref == e.selfRef {
		return true
	}
	return containsRef(except, ref)
}

// dispatchGroups sends one shared frame to each per-loop group. It releases the
// frame's initial reference when done.
func (s *Server) dispatchGroups(st *serverState, groups []room.Group, f *connection.Frame) error {
	firstErr := error(nil)
	for _, g := range groups {
		if int(g.Loop) >= len(st.loops) || st.loops[g.Loop] == nil {
			continue
		}
		batch := connection.AcquireBatch(len(g.Refs))
		batch.Refs = append(batch.Refs, g.Refs...)
		f.Retain()
		if !st.loops[g.Loop].Enqueue(reactor.Command{Kind: reactor.CmdFanout, Frame: f, Batch: batch}) {
			f.Release()
			batch.Release()
			s.backpressure.Add(1)
			firstErr = ErrBackpressure
		}
	}
	f.Release()
	return firstErr
}

func (s *Server) connForRef(st *serverState, ref connection.Ref) *Conn {
	if int(ref.Loop) >= len(st.conns) {
		return nil
	}
	c := st.conns[ref.Loop].get(ref.Slot)
	if c == nil || c.gen != ref.Gen {
		return nil
	}
	return c
}

func (e Emitter) nativeFrame(eventID uint32, payload []byte, requestID uint64) *connection.Frame {
	f := connection.BuildFrame(event.KindEvent, eventID, requestID, 0, payload)
	f.Volatile = e.volatile
	return f
}

func (e Emitter) protoMessageFrame(eventID uint32, payload []byte, requestID uint64) *connection.Frame {
	p := protocol.Push{
		Kind:    protocol.PushMessage,
		Message: protocol.Message{EventID: eventID, Payload: payload, RequestID: requestID},
	}
	return e.protoFrame(&p)
}

func (e Emitter) protoEventFrame(roomName string, eventID uint32, payload []byte, requestID uint64) *connection.Frame {
	p := protocol.Push{
		Kind: protocol.PushEvent,
		Event: protocol.EventPush{
			Room:      []byte(roomName),
			EventID:   eventID,
			Payload:   payload,
			RequestID: requestID,
		},
	}
	return e.protoFrame(&p)
}

func (e Emitter) protoFrame(p *protocol.Push) *connection.Frame {
	size := protocol.FramedPushSize(p)
	f, h := connection.NewFrameWithHeader(size)
	out := protocol.AppendFramedPush(f.Buf[h:h], p)
	f.Buf = f.Buf[:h+len(out)]
	f.Counted = true
	f.Volatile = e.volatile
	return f
}

func containsRef(refs []connection.Ref, want connection.Ref) bool {
	for _, ref := range refs {
		if ref == want {
			return true
		}
	}
	return false
}

// To returns an Emitter targeting a room from the server.
func (s *Server) To(room string) Emitter {
	return Emitter{srv: s, hasRoom: true, room: room}
}

// ToConns returns an Emitter targeting the given connection IDs. This is the
// Self-Room unicast path.
func (s *Server) ToConns(ids ...ConnID) Emitter {
	return Emitter{srv: s, ids: ids}
}

// Emit sends an event to every open connection.
func (s *Server) Emit(event string, payload []byte) error {
	return s.EmitID(EventIDForName(event), payload)
}

// EmitID is the numeric low-level form of Emit.
func (s *Server) EmitID(eventID uint32, payload []byte) error {
	if s == nil {
		return ErrClosed
	}
	if int64(len(payload)) > s.cfg.MaxMessageSize-event.HeaderSize {
		return ErrPayloadTooLarge
	}
	e := Emitter{srv: s, all: true}
	return e.dispatch(eventID, payload, 0)
}

// RoomSize reports the number of members in a room.
func (s *Server) RoomSize(name string) int {
	if s == nil {
		return 0
	}
	return s.rooms.Size(name)
}
