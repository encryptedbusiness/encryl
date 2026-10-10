//go:build linux

package app

import (
	"sync/atomic"
	"time"
)

// Stats is a point-in-time snapshot of server counters.
type Stats struct {
	ConnectionsAccepted uint64
	ConnectionsOpen     int
	ConnectionsClosed   uint64

	MessagesIn  uint64
	MessagesOut uint64
	BytesIn     uint64
	BytesOut    uint64

	Rooms              int
	Memberships        int
	ProtocolErrors     uint64
	AcceptErrors       uint64
	RecvErrors         uint64
	SendErrors         uint64
	FileTableErrors    uint64
	ReadPauses         uint64
	IllegalTransitions uint64
	CQOverflow         uint64
	BufferExhausted    uint64
	EventFDErrors      uint64
	EventFDErrno       int32
	WakeErrors         uint64
	StaleCompletions   uint64
	TimerExpired       uint64
	Submissions        uint64
	Completions        uint64

	SlowConsumers   uint64
	Backpressure    uint64
	DroppedMessages uint64
	Evicted         uint64
	StaleSends      uint64
	StaleAcks       uint64
	AckTimeouts     uint64
	CloseSubmitted  uint64
	CloseCompleted  uint64
	CloseErrors     uint64
	CloseErrno      int32
	FanoutCommands  uint64
	FanoutRefs      uint64

	// WriteQueueDepth and WriteQueueBytes are the current queued totals across
	// all connections.
	WriteQueueDepth int64
	WriteQueueBytes int64

	// PendingAcks is the number of outstanding EmitWithAck requests.
	PendingAcks int

	// RejectedByEvent counts handler errors and unknown events, keyed by
	// event ID.
	RejectedByEvent map[uint32]uint64

	StartedAt time.Time
	Uptime    time.Duration
}

// BytesOut returns the total bytes written to sockets. It is a cheap counter
// read for harnesses: unlike Stats it does not walk the room directory.
func (s *Server) BytesOut() uint64 {
	if s == nil {
		return 0
	}
	state := s.state.Load()
	if state == nil {
		return 0
	}
	var n uint64
	for _, l := range state.loops {
		if l != nil {
			n += l.Stats().BytesOut
		}
	}
	return n
}

// Stats returns a snapshot of server counters. Reading it does not block the
// loops: counters are per-loop and aggregated lazily.
func (s *Server) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	st := Stats{
		StartedAt:       s.startedAt,
		Uptime:          time.Since(s.startedAt),
		Backpressure:    s.backpressure.Load(),
		DroppedMessages: s.dropped.Load(),
		SlowConsumers:   s.slowConsumers.Load(),
		StaleAcks:       s.staleAcks.Load(),
		PendingAcks:     s.pending.Len(),
	}
	state := s.state.Load()
	if state == nil {
		st.Rooms, st.Memberships = s.rooms.Counts()
		s.fillRejections(&st)
		return st
	}
	for _, l := range state.loops {
		if l == nil {
			continue
		}
		ls := l.Stats()
		st.ConnectionsAccepted += ls.Accepted
		st.ConnectionsClosed += ls.Closed
		st.ConnectionsOpen += ls.Live
		st.MessagesIn += ls.MessagesIn
		st.MessagesOut += ls.MessagesOut
		st.BytesIn += ls.BytesIn
		st.BytesOut += ls.BytesOut
		st.AcceptErrors += ls.AcceptErrors
		st.RecvErrors += ls.RecvErrors
		st.SendErrors += ls.SendErrors
		st.IllegalTransitions += ls.IllegalTransitions
		st.CQOverflow += ls.CQOverflow
		st.BufferExhausted += ls.BufferExhausted
		st.EventFDErrors += ls.EventFDErrors
		if st.EventFDErrno == 0 {
			st.EventFDErrno = ls.EventFDErrno
		}
		st.WakeErrors += ls.WakeErrors
		st.StaleCompletions += ls.StaleCompletions
		st.TimerExpired += ls.TimerExpired
		st.Submissions += ls.Submissions
		st.Completions += ls.Completions
		st.Backpressure += ls.Backpressured
		st.DroppedMessages += ls.Dropped
		st.Evicted += ls.Evicted
		st.StaleSends += ls.StaleSends
		st.AckTimeouts += ls.AckTimeouts
		st.CloseSubmitted += ls.CloseSubmitted
		st.CloseCompleted += ls.CloseCompleted
		st.CloseErrors += ls.CloseErrors
		if st.CloseErrno == 0 {
			st.CloseErrno = ls.CloseErrno
		}
		st.FanoutCommands += ls.FanoutCommands
		st.FanoutRefs += ls.FanoutRefs
		st.WriteQueueDepth += ls.Queued
		st.WriteQueueBytes += ls.QueuedBytes
		st.FileTableErrors += ls.FileTableErrors
		st.ReadPauses += ls.ReadPauses
		st.ProtocolErrors += ls.RecvErrors + ls.SendErrors + ls.IllegalTransitions
	}
	st.Rooms, st.Memberships = s.rooms.Counts()
	s.fillRejections(&st)
	return st
}

func (s *Server) fillRejections(st *Stats) {
	s.rejected.Range(func(k uint32, v *atomic.Uint64) bool {
		if st.RejectedByEvent == nil {
			st.RejectedByEvent = make(map[uint32]uint64, 8)
		}
		st.RejectedByEvent[k] = v.Load()
		return true
	})
}
