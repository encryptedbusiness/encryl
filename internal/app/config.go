//go:build linux

package app

import (
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/encryptedbusiness/encryl/connslab"
)

// BackpressurePolicy decides what happens when a connection's bounded write
// queue cannot accept another message.
type BackpressurePolicy int

const (
	// BackpressureReject returns ErrBackpressure to the caller and leaves the
	// connection open. The default: callers get to decide.
	BackpressureReject BackpressurePolicy = iota

	// BackpressureDropNewest accepts the send but discards it. Useful for
	// lossy telemetry streams where the newest sample matters least.
	BackpressureDropNewest

	// BackpressureDropOldest evicts the oldest queued message and enqueues the
	// new one. This keeps latency bounded for state-snapshot streams.
	BackpressureDropOldest

	// BackpressureCloseSlow closes the connection with a slow-consumer close
	// code. Use it when an unresponsive peer must not be allowed to consume
	// server memory.
	BackpressureCloseSlow
)

// Config configures a Server. The zero value is not usable: use
// DefaultConfig and override what you need.
//
// Every field is validated by New. Invalid combinations are rejected with a
// *ConfigError listing all problems at once, rather than failing at the first
// one.
//
// The server speaks plaintext ws:// only. TLS terminates at a load balancer or
// reverse proxy in front of the process; the library does not terminate,
// offload, or negotiate TLS. See docs/adr/0013-tls-terminates-upstream.md.
type Config struct {
	// Addr is the TCP listen address, for example ":8080" or "127.0.0.1:0".
	Addr string

	// Loops is the number of event loops. Zero means runtime.NumCPU(). Each
	// loop owns a set of connections outright and is pinned to a core.
	Loops int

	// ReusePort sets SO_REUSEPORT so every loop can accept from the same
	// address. Default true.
	ReusePort bool

	// AcceptBacklog is the listen backlog. Zero means 4096.
	AcceptBacklog int

	// AllowedOrigins is the Origin allowlist for browser clients. An entry may
	// be "*" or an http/https origin such as "https://app.example.com".
	// An empty list means same-origin only, which is the safe default for a
	// browser-facing deployment behind a reverse proxy.
	//
	// Because the library does not terminate TLS, this allowlist is the only
	// in-process defense against cross-site WebSocket hijacking.
	AllowedOrigins []string

	// MaxFrameSize is the largest single WebSocket frame accepted, in bytes.
	// Default 1 MiB. Must be at least 125 to fit a control frame.
	MaxFrameSize int64

	// MaxMessageSize is the largest reassembled message accepted, in bytes.
	// Default 4 MiB. A message is the concatenation of all fragments.
	MaxMessageSize int64

	// MaxFragments bounds the number of continuation frames in one message.
	// Default 128. It exists to stop fragment-count attacks that stay under
	// the byte limit.
	MaxFragments int

	// MaxHeaderBytes bounds the HTTP upgrade request header. Default 8192.
	MaxHeaderBytes int

	// HandshakeTimeout bounds the TCP connect to completed WebSocket upgrade.
	// Default 10s.
	HandshakeTimeout time.Duration

	// IdleTimeout closes connections that send no frame for this long. Must be
	// greater than PingInterval. Default 2m.
	IdleTimeout time.Duration

	// PingInterval is how often the server sends a WebSocket ping. Zero
	// disables server pings. Default 30s.
	PingInterval time.Duration

	// CloseTimeout bounds the closing handshake before the connection is
	// aborted. Default 5s.
	CloseTimeout time.Duration

	// ShutdownTimeout bounds the whole graceful shutdown, including draining
	// writes and closing every descriptor. Default 30s.
	ShutdownTimeout time.Duration

	// WriteQueueBytes is the per-connection outbound byte budget. Default
	// 256 KiB.
	WriteQueueBytes int64

	// WriteQueueMessages is the per-connection outbound message limit.
	// Default 64.
	WriteQueueMessages int

	// Backpressure selects the full-queue behaviour. Default
	// BackpressureReject.
	Backpressure BackpressurePolicy

	// MaxPendingAcks bounds the outstanding EmitWithAck slab. A full slab
	// returns ErrBackpressure. Default 65536.
	MaxPendingAcks int

	// EventNames maps event names to numeric IDs for the Bebop protocol.
	// Names are resolved before traffic; the wire uses the numeric ID.
	EventNames map[string]uint32

	// ChannelEvents maps a room name to its default event ID. A Bebop
	// publish or subscribe that does not name an event uses this mapping.
	ChannelEvents map[string]uint32

	// TokenVerifier authenticates a Bebop Connect token. A nil verifier
	// accepts every connection, matching the trusted-proxy model.
	TokenVerifier func(token []byte) error

	// MaxRoomsPerConn caps room memberships per connection. Zero disables the
	// limit. Default 128.
	MaxRoomsPerConn int

	// ReadBufferSize is the size of one read buffer taken from the per-loop
	// arena while a receive is in flight. Default 1024. Idle connections hold
	// no read buffer.
	ReadBufferSize int

	// BufferCount is the number of read buffers in each loop's arena. Default
	// 256. This is a fixed per-loop cost, not a per-connection cost.
	BufferCount int

	// TxBytes is the size of each loop's outbound arena. Default 1 MiB + 64.
	// It must be large enough to hold one maximum-size frame plus framing
	// overhead.
	TxBytes int64

	// MaxConnections caps the connection slab. Default 1,000,000. The slab is
	// allocated in chunks, so this is a ceiling rather than an upfront cost.
	MaxConnections int
}

// DefaultConfig returns a configuration with safe production defaults.
//
// The server speaks plaintext ws:// only; TLS is terminated in front of it.
func DefaultConfig() Config {
	return Config{
		Addr:               ":8080",
		Loops:              0,
		ReusePort:          true,
		AcceptBacklog:      4096,
		MaxFrameSize:       1 << 20,
		MaxMessageSize:     4 << 20,
		MaxFragments:       128,
		MaxHeaderBytes:     8 << 10,
		HandshakeTimeout:   10 * time.Second,
		IdleTimeout:        2 * time.Minute,
		PingInterval:       30 * time.Second,
		CloseTimeout:       5 * time.Second,
		ShutdownTimeout:    30 * time.Second,
		WriteQueueBytes:    4 << 20,
		WriteQueueMessages: 64,
		Backpressure:       BackpressureReject,
		MaxPendingAcks:     65536,
		MaxRoomsPerConn:    128,
		ReadBufferSize:     1024,
		BufferCount:        256,
		TxBytes:            (1 << 20) + 64,
		MaxConnections:     1_000_000,
		TokenVerifier:      func([]byte) error { return nil },
	}
}

// normalize merges zero values from DefaultConfig. It does not hide missing
// required values; Validate still rejects them.
func (c *Config) normalize() {
	def := DefaultConfig()
	if c.Loops == 0 {
		if def.Loops != 0 {
			c.Loops = def.Loops
		} else {
			c.Loops = runtime.NumCPU()
		}
	}
	if c.AcceptBacklog == 0 {
		c.AcceptBacklog = def.AcceptBacklog
	}
	if c.MaxFrameSize == 0 {
		c.MaxFrameSize = def.MaxFrameSize
	}
	if c.MaxMessageSize == 0 {
		c.MaxMessageSize = def.MaxMessageSize
	}
	if c.MaxFragments == 0 {
		c.MaxFragments = def.MaxFragments
	}
	if c.MaxHeaderBytes == 0 {
		c.MaxHeaderBytes = def.MaxHeaderBytes
	}
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = def.HandshakeTimeout
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = def.IdleTimeout
	}
	if c.CloseTimeout == 0 {
		c.CloseTimeout = def.CloseTimeout
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = def.ShutdownTimeout
	}
	if c.WriteQueueBytes == 0 {
		c.WriteQueueBytes = def.WriteQueueBytes
	}
	if c.WriteQueueMessages == 0 {
		c.WriteQueueMessages = def.WriteQueueMessages
	}
	if c.MaxPendingAcks == 0 {
		c.MaxPendingAcks = def.MaxPendingAcks
	}
	if c.MaxRoomsPerConn == 0 {
		c.MaxRoomsPerConn = def.MaxRoomsPerConn
	}
	if c.ReadBufferSize == 0 {
		c.ReadBufferSize = def.ReadBufferSize
	}
	if c.BufferCount == 0 {
		c.BufferCount = def.BufferCount
	}
	if c.TxBytes == 0 {
		c.TxBytes = def.TxBytes
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = def.MaxConnections
	}
}

func configProblem(field string, value any, rule, format string, args ...any) ConfigProblem {
	return ConfigProblem{Field: field, Value: value, Rule: rule, Message: fmt.Sprintf(format, args...)}
}

// Validate reports every invalid field at once. Warnings are available from
// ConfigWarnings and never make Validate fail.
func (c *Config) Validate() error {
	problems, warnings := c.validate()
	if len(problems) == 0 {
		return nil
	}
	return &ConfigError{Problems: problems, Warnings: warnings}
}

func (c *Config) validate() (problems, warnings []ConfigProblem) {
	add := func(field string, value any, rule, format string, args ...any) {
		problems = append(problems, configProblem(field, value, rule, format, args...))
	}
	warn := func(field string, value any, rule, format string, args ...any) {
		warnings = append(warnings, configProblem(field, value, rule, format, args...))
	}

	if strings.TrimSpace(c.Addr) == "" {
		add("Addr", c.Addr, "required", "Addr is required")
	} else if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		add("Addr", c.Addr, "host_port", "Addr %q is not host:port: %v", c.Addr, err)
	}

	if c.Loops <= 0 {
		add("Loops", c.Loops, "range", "Loops must be > 0 after normalization (got %d)", c.Loops)
	} else if c.Loops > 1024 {
		add("Loops", c.Loops, "range", "Loops %d exceeds the supported maximum of 1024", c.Loops)
	}
	if c.AcceptBacklog <= 0 {
		add("AcceptBacklog", c.AcceptBacklog, "range", "AcceptBacklog must be > 0")
	}
	if !c.ReusePort && c.Loops > 1 {
		add("ReusePort", c.ReusePort, "cross_field", "ReusePort must be true when Loops > 1")
	}

	originSeen := make(map[string]struct{}, len(c.AllowedOrigins))
	for _, origin := range c.AllowedOrigins {
		if _, dup := originSeen[origin]; dup {
			warn("AllowedOrigins", origin, "duplicate", "AllowedOrigins contains duplicate entry %q", origin)
		}
		originSeen[origin] = struct{}{}
		if origin == "*" {
			warn("AllowedOrigins", origin, "wildcard", "AllowedOrigins contains \"*\"")
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			add("AllowedOrigins", origin, "origin", "AllowedOrigins entry %q must be \"*\" or an http(s) origin", origin)
		}
	}

	if c.MaxFrameSize < 125 {
		add("MaxFrameSize", c.MaxFrameSize, "range", "MaxFrameSize must be >= 125 (got %d)", c.MaxFrameSize)
	}
	if c.MaxMessageSize <= 0 {
		add("MaxMessageSize", c.MaxMessageSize, "range", "MaxMessageSize must be > 0")
	} else if c.MaxMessageSize > 1<<30 {
		add("MaxMessageSize", c.MaxMessageSize, "range", "MaxMessageSize must be <= 1 GiB (got %d)", c.MaxMessageSize)
	}
	if c.MaxFrameSize > c.MaxMessageSize {
		add("MaxFrameSize", c.MaxFrameSize, "cross_field", "MaxFrameSize (%d) must be <= MaxMessageSize (%d)", c.MaxFrameSize, c.MaxMessageSize)
	}
	if c.MaxFragments <= 0 {
		add("MaxFragments", c.MaxFragments, "range", "MaxFragments must be > 0")
	} else if c.MaxFragments > 1024 {
		add("MaxFragments", c.MaxFragments, "range", "MaxFragments must be <= 1024 (got %d)", c.MaxFragments)
	}
	if c.MaxHeaderBytes < 1024 {
		add("MaxHeaderBytes", c.MaxHeaderBytes, "range", "MaxHeaderBytes must be >= 1024 (got %d)", c.MaxHeaderBytes)
	}
	if c.MaxHeaderBytes > 1<<20 {
		add("MaxHeaderBytes", c.MaxHeaderBytes, "range", "MaxHeaderBytes must be <= 1 MiB (got %d)", c.MaxHeaderBytes)
	}

	for _, item := range []struct {
		name string
		d    time.Duration
	}{
		{"HandshakeTimeout", c.HandshakeTimeout},
		{"IdleTimeout", c.IdleTimeout},
		{"CloseTimeout", c.CloseTimeout},
		{"ShutdownTimeout", c.ShutdownTimeout},
	} {
		if item.d <= 0 {
			add(item.name, item.d, "range", "%s must be > 0", item.name)
		}
	}
	if c.PingInterval < 0 {
		add("PingInterval", c.PingInterval, "range", "PingInterval must be >= 0")
	} else if c.PingInterval == 0 && c.IdleTimeout > 0 {
		warn("PingInterval", c.PingInterval, "no_dead_peer_detection", "PingInterval is zero; dead peers are not detected")
	} else if c.PingInterval >= c.IdleTimeout {
		add("PingInterval", c.PingInterval, "cross_field", "PingInterval (%s) must be less than IdleTimeout (%s)", c.PingInterval, c.IdleTimeout)
	}

	if c.WriteQueueBytes <= 0 {
		add("WriteQueueBytes", c.WriteQueueBytes, "range", "WriteQueueBytes must be > 0")
	} else if c.MaxFrameSize > 0 && c.WriteQueueBytes < c.MaxFrameSize+64 {
		add("WriteQueueBytes", c.WriteQueueBytes, "cross_field", "WriteQueueBytes (%d) must be at least MaxFrameSize + 64 (%d)", c.WriteQueueBytes, c.MaxFrameSize+64)
	}
	if c.WriteQueueMessages <= 0 {
		add("WriteQueueMessages", c.WriteQueueMessages, "range", "WriteQueueMessages must be > 0")
	}
	if c.Backpressure < BackpressureReject || c.Backpressure > BackpressureCloseSlow {
		add("Backpressure", c.Backpressure, "range", "Backpressure %d is out of range", c.Backpressure)
	}

	eventIDs := make(map[uint32]string, len(c.EventNames))
	for name, id := range c.EventNames {
		if name == "" || id == 0 {
			add("EventNames", name, "entry", "EventNames entry %q must have a non-empty name and a non-zero ID", name)
			continue
		}
		if other, ok := eventIDs[id]; ok && other != name {
			add("EventNames", name, "duplicate_id", "EventNames names %q and %q share ID %d", other, name, id)
		}
		eventIDs[id] = name
	}
	for name, id := range c.ChannelEvents {
		if name == "" || id == 0 {
			add("ChannelEvents", name, "entry", "ChannelEvents entry %q must have a non-empty name and a non-zero ID", name)
			continue
		}
		if _, ok := eventIDs[id]; !ok {
			warn("ChannelEvents", name, "orphan_id", "ChannelEvents room %q uses event ID %d that is not in EventNames", name, id)
		}
	}
	if c.TokenVerifier == nil {
		warn("TokenVerifier", nil, "accept_all", "TokenVerifier is nil; every Bebop token is accepted")
	}
	if c.MaxMessageSize > c.WriteQueueBytes {
		warn("MaxMessageSize", c.MaxMessageSize, "slow_path", "MaxMessageSize exceeds WriteQueueBytes; a full-size emit takes the slow path")
	}

	if c.MaxPendingAcks <= 0 {
		add("MaxPendingAcks", c.MaxPendingAcks, "range", "MaxPendingAcks must be > 0")
	} else if c.MaxPendingAcks > 1<<28 {
		add("MaxPendingAcks", c.MaxPendingAcks, "range", "MaxPendingAcks %d exceeds the supported maximum of %d", c.MaxPendingAcks, 1<<28)
	}
	if c.MaxRoomsPerConn < 0 {
		add("MaxRoomsPerConn", c.MaxRoomsPerConn, "range", "MaxRoomsPerConn must be >= 0")
	}

	if c.ReadBufferSize < 256 {
		add("ReadBufferSize", c.ReadBufferSize, "range", "ReadBufferSize must be >= 256 (got %d)", c.ReadBufferSize)
	}
	if int64(c.ReadBufferSize) > c.MaxFrameSize && c.MaxFrameSize > 0 {
		add("ReadBufferSize", c.ReadBufferSize, "cross_field", "ReadBufferSize (%d) must be <= MaxFrameSize (%d)", c.ReadBufferSize, c.MaxFrameSize)
	}
	if c.BufferCount <= 0 {
		add("BufferCount", c.BufferCount, "range", "BufferCount must be > 0")
	}
	if c.TxBytes <= 0 {
		add("TxBytes", c.TxBytes, "range", "TxBytes must be > 0")
	} else if c.TxBytes < c.MaxFrameSize+64 {
		add("TxBytes", c.TxBytes, "cross_field", "TxBytes (%d) must be at least MaxFrameSize + 64 (%d)", c.TxBytes, c.MaxFrameSize+64)
	}
	if c.MaxConnections <= 0 {
		add("MaxConnections", c.MaxConnections, "range", "MaxConnections must be > 0")
	} else if c.MaxConnections > int(connslab.SlotLimit) {
		add("MaxConnections", c.MaxConnections, "slot_limit", "MaxConnections %d exceeds the 24-bit slot limit of %d", c.MaxConnections, connslab.SlotLimit)
	} else if c.Loops > 0 && c.MaxConnections < c.Loops {
		add("MaxConnections", c.MaxConnections, "cross_field", "MaxConnections %d must be >= Loops %d", c.MaxConnections, c.Loops)
	}
	return problems, warnings
}

// ConfigWarnings returns the non-fatal configuration warnings.
func (s *Server) ConfigWarnings() []ConfigProblem {
	if s == nil {
		return nil
	}
	return append([]ConfigProblem(nil), s.configWarnings...)
}
