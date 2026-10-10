//go:build linux

package app

import (
	"context"
	"errors"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/netio"
	"github.com/encryptedbusiness/encryl/reactor"
)

var errAlreadyStarted = errors.New("encryl: server already started")

// Start initializes the reactor loops without binding a listener. The HTTP
// framework owns the listener; this is the attached mode.
func (s *Server) Start(ctx context.Context) error {
	if s == nil {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.start(ctx, nil)
}

func (s *Server) needsConnAtOpen() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.connectMW) > 0 ||
		s.connectHandler.Load() != nil ||
		s.disconnectHandler.Load() != nil ||
		s.lifecyclePtr.Load() != nil
}

func (s *Server) start(ctx context.Context, listeners []netio.Listener) error {
	if !s.started.CompareAndSwap(false, true) {
		return errAlreadyStarted
	}
	if !s.running.CompareAndSwap(false, true) {
		return errors.New("encryl: server already running")
	}

	loops := s.cfg.Loops
	if loops < 1 {
		loops = 1
	}
	if listeners != nil && len(listeners) < loops {
		s.runMu.Lock()
		s.runErr = errors.New("encryl: not enough listeners")
		s.runMu.Unlock()
		s.running.Store(false)
		s.doneOnce.Do(func() { close(s.done) })
		return errors.New("encryl: not enough listeners")
	}
	s.events.Freeze()
	s.eventNames.Freeze()

	perLoop := (s.cfg.MaxConnections + loops - 1) / loops
	if perLoop < 1 {
		perLoop = 1
	}
	conns := make([]*connTable, loops)
	for i := range conns {
		conns[i] = newConnTable(1024)
	}
	loopList := make([]reactor.Loop, loops)

	for i := 0; i < loops; i++ {
		idx := i
		var ln netio.Listener
		if listeners != nil {
			ln = listeners[idx]
		}
		l, err := reactor.NewLoop(reactor.LoopConfig{
			Index:              idx,
			Listener:           ln,
			ReadBufferSize:     s.cfg.ReadBufferSize,
			BufferCount:        s.cfg.BufferCount,
			TxBytes:            s.cfg.TxBytes,
			MaxConnections:     perLoop,
			RingEntries:        32768,
			WriteQueueMessages: s.cfg.WriteQueueMessages,
			WriteQueueBytes:    s.cfg.WriteQueueBytes,
			Backpressure:       connection.BackpressurePolicy(s.cfg.Backpressure),
			MaxPendingAcks:     s.cfg.MaxPendingAcks,
			HandshakeTimeout:   s.cfg.HandshakeTimeout,
			IdleTimeout:        s.cfg.IdleTimeout,
			PingInterval:       s.cfg.PingInterval,
			CloseTimeout:       s.cfg.CloseTimeout,
			ShutdownTimeout:    s.cfg.ShutdownTimeout,
			MaxHeaderBytes:     s.cfg.MaxHeaderBytes,
			MaxFrameSize:       s.cfg.MaxFrameSize,
			MaxMessageSize:     s.cfg.MaxMessageSize,
			MaxFragments:       s.cfg.MaxFragments,
			AllowedOrigins:     s.cfg.AllowedOrigins,
			OnOpen: func(slot, gen uint32, acct *connection.Accounting, ip [16]byte, port uint16, family uint8, encoding uint8) {
				s.handleOpen(idx, slot, gen, acct, ip, port, family, encoding)
			},
			OnClose: func(slot, gen uint32, code uint16, reason string, remote bool) {
				s.handleClose(idx, slot, gen, code, reason, remote)
			},
			OnMessage: func(slot, gen uint32, payload []byte) {
				s.handleMessage(idx, slot, gen, payload)
			},
			OnAckTimeout: s.handleAckTimeout,
		})
		if err != nil {
			s.running.Store(false)
			s.doneOnce.Do(func() { close(s.done) })
			return err
		}
		loopList[idx] = l
	}

	s.state.Store(&serverState{
		loops:      loopList,
		conns:      conns,
		perLoop:    uint32(perLoop),
		connAtOpen: s.needsConnAtOpen(),
	})

	for _, l := range loopList {
		l := l
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := l.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.setRunErr(err)
				s.stopOnce.Do(func() { close(s.stopCh) })
			}
		}()
	}

	go func() {
		s.wg.Wait()
		s.state.Store(nil)
		s.running.Store(false)
		s.doneOnce.Do(func() { close(s.done) })
	}()
	go func() {
		select {
		case <-ctx.Done():
			s.stopOnce.Do(func() { close(s.stopCh) })
		case <-s.done:
		}
	}()
	go func() {
		select {
		case <-s.stopCh:
			s.shutdownLoops()
		case <-s.done:
		}
	}()
	return nil
}
