//go:build linux

package app

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/netio"
	"github.com/encryptedbusiness/encryl/protocol"
	"github.com/encryptedbusiness/encryl/reactor"
	"github.com/encryptedbusiness/encryl/room"
	"github.com/puzpuzpuz/xsync/v4"
)

type serverState struct {
	loops      []reactor.Loop
	conns      []*connTable
	perLoop    uint32
	connAtOpen bool
}

// connTable maps a loop-local slot to its public Conn. Chunks are allocated
// only for slots that actually carry a connection.
type connTable struct {
	chunks    [][]atomic.Pointer[Conn]
	chunkSize uint32
}

func newConnTable(chunkSize int) *connTable {
	if chunkSize <= 0 {
		chunkSize = 4096
	}
	return &connTable{chunkSize: uint32(chunkSize)}
}

func (t *connTable) get(slot uint32) *Conn {
	ci := slot / t.chunkSize
	if int(ci) >= len(t.chunks) {
		return nil
	}
	return t.chunks[ci][slot%t.chunkSize].Load()
}

func (t *connTable) set(slot uint32, c *Conn) {
	ci := slot / t.chunkSize
	for int(ci) >= len(t.chunks) {
		t.chunks = append(t.chunks, make([]atomic.Pointer[Conn], t.chunkSize))
	}
	t.chunks[ci][slot%t.chunkSize].Store(c)
}

func (t *connTable) clear(slot uint32) {
	ci := slot / t.chunkSize
	if int(ci) < len(t.chunks) {
		t.chunks[ci][slot%t.chunkSize].Store(nil)
	}
}

// Server owns listeners, loops, and connection state. Create it with New.
type Server struct {
	cfg            Config
	configWarnings []ConfigProblem

	mu                sync.RWMutex
	events            *event.Registry[*Conn]
	lifecyclePtr      atomic.Pointer[Handler]
	connectHandler    atomic.Pointer[ConnectHandler]
	disconnectHandler atomic.Pointer[DisconnectHandler]
	connectMW         []ConnectMiddleware

	startedAt     time.Time
	rooms         *room.Directory
	pending       *event.Pending
	eventNames    *protocol.EventRegistry
	channelEvents map[string]uint32
	tokenVerify   func([]byte) error
	connCounts    [2]atomic.Int64

	state atomic.Pointer[serverState]

	stopOnce sync.Once
	doneOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}

	started atomic.Bool
	running atomic.Bool

	wg     sync.WaitGroup
	runMu  sync.Mutex
	runErr error

	backpressure  atomic.Uint64
	dropped       atomic.Uint64
	slowConsumers atomic.Uint64
	staleAcks     atomic.Uint64
	attachSeq     atomic.Uint32
	rejected      *xsync.Map[uint32, *atomic.Uint64]
}

// mustLinux refuses to start anywhere else. A build on another operating
// system compiles, so the failure is a clear panic at the one entry point
// rather than a silent change of behavior or a confusing bind error.
func mustLinux() {
	if runtime.GOOS != "linux" {
		panic(fmt.Sprintf("encryl: Linux with io_uring is required; this is %s/%s",
			runtime.GOOS, runtime.GOARCH))
	}
}

// New validates cfg and constructs a Server. It performs no I/O: binding
// happens in ListenAndServe, so configuration errors are reported here and
// deployment errors there.
func perLoopFor(cfg Config) uint32 {
	loops := cfg.Loops
	if loops < 1 {
		loops = 1
	}
	return uint32((cfg.MaxConnections + loops - 1) / loops)
}

func maxSlotsFor(cfg Config) int {
	loops := cfg.Loops
	if loops < 1 {
		loops = 1
	}
	return int(perLoopFor(cfg)) * loops
}

// New panics on a non-Linux host. See the package documentation.
func New(cfg Config) (*Server, error) {
	mustLinux()
	cfg.normalize()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	_, warnings := cfg.validate()

	names := protocol.NewEventRegistry()
	for name, id := range cfg.EventNames {
		if err := names.Register(name, id); err != nil {
			return nil, err
		}
	}
	channels := make(map[string]uint32, len(cfg.ChannelEvents))
	for name, id := range cfg.ChannelEvents {
		channels[name] = id
	}

	return &Server{
		cfg:            cfg,
		events:         event.NewRegistry[*Conn](),
		startedAt:      time.Now(),
		rooms:          room.NewSized(cfg.MaxRoomsPerConn, perLoopFor(cfg), maxSlotsFor(cfg)),
		pending:        event.NewPending(cfg.MaxPendingAcks),
		eventNames:     names,
		channelEvents:  channels,
		tokenVerify:    cfg.TokenVerifier,
		configWarnings: warnings,
		stopCh:         make(chan struct{}),
		rejected:       xsync.NewMap[uint32, *atomic.Uint64](),
		done:           make(chan struct{}),
	}, nil
}

// Config returns a copy of the effective configuration. Slices are cloned so
// callers cannot mutate server state.
func (s *Server) Config() Config {
	if s == nil {
		return Config{}
	}
	out := s.cfg
	if out.AllowedOrigins != nil {
		out.AllowedOrigins = append([]string(nil), out.AllowedOrigins...)
	}
	if out.EventNames != nil {
		m := make(map[string]uint32, len(out.EventNames))
		for k, v := range out.EventNames {
			m[k] = v
		}
		out.EventNames = m
	}
	if out.ChannelEvents != nil {
		m := make(map[string]uint32, len(out.ChannelEvents))
		for k, v := range out.ChannelEvents {
			m[k] = v
		}
		out.ChannelEvents = m
	}
	return out
}

// Stats returns a snapshot of server counters. Reading it does not block the
// loops: counters are per-loop and aggregated lazily.
// ListenAndServe binds Addr and serves until ctx is cancelled or Shutdown is
// called. It returns nil after a clean shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if s == nil {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	loops := s.cfg.Loops
	if loops < 1 {
		loops = 1
	}
	listeners, err := s.bindListeners(loops)
	if err != nil {
		return err
	}
	if err := s.start(ctx, listeners); err != nil {
		s.closeListeners(listeners)
		return err
	}

	select {
	case <-ctx.Done():
	case <-s.stopCh:
	}

	s.shutdownLoops()
	s.wg.Wait()
	s.closeListeners(listeners)
	s.state.Store(nil)

	s.runMu.Lock()
	err = s.runErr
	s.runMu.Unlock()
	return err
}

// Shutdown stops accepting, sends close frames, drains in-flight work until
// ctx or Config.ShutdownTimeout expires, closes every descriptor exactly once,
// and joins every loop. It is idempotent and safe to call concurrently with
// ListenAndServe.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return ErrClosed
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.shutdownLoops()
	if !s.started.Load() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.cfg.ShutdownTimeout):
		return errors.New("encryl: shutdown timed out")
	}
}

func (s *Server) bindListeners(n int) ([]netio.Listener, error) {
	first, err := netio.Listen(s.cfg.Addr, s.cfg.ReusePort, s.cfg.AcceptBacklog)
	if err != nil {
		return nil, err
	}
	out := make([]netio.Listener, 0, n)
	out = append(out, first)
	addr := first.Addr().String()
	for i := 1; i < n; i++ {
		ln, err := netio.Listen(addr, s.cfg.ReusePort, s.cfg.AcceptBacklog)
		if err != nil {
			s.closeListeners(out)
			return nil, err
		}
		out = append(out, ln)
	}
	return out, nil
}

func (s *Server) closeListeners(listeners []netio.Listener) {
	for _, ln := range listeners {
		if ln != nil {
			_ = ln.Close()
		}
	}
}

func (s *Server) shutdownLoops() {
	st := s.state.Load()
	if st == nil {
		return
	}
	for _, l := range st.loops {
		if l != nil {
			_ = l.Shutdown()
		}
	}
}

func (s *Server) setRunErr(err error) {
	s.runMu.Lock()
	if s.runErr == nil {
		s.runErr = err
	}
	s.runMu.Unlock()
}

func (s *Server) globalSlot(loop int, slot uint32) uint32 {
	st := s.state.Load()
	if st == nil || st.perLoop == 0 {
		return slot
	}
	return uint32(loop)*st.perLoop + slot
}

func (s *Server) enqueue(loop int, cmd reactor.Command) error {
	if !s.enqueueChecked(loop, cmd) {
		s.backpressure.Add(1)
		return ErrBackpressure
	}
	return nil
}

func (s *Server) enqueueChecked(loop int, cmd reactor.Command) bool {
	st := s.state.Load()
	if st == nil || loop < 0 || loop >= len(st.loops) || st.loops[loop] == nil {
		return false
	}
	return st.loops[loop].Enqueue(cmd)
}
