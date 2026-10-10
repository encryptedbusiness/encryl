//go:build linux

package app

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func loopbackConfig() Config {
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	return cfg
}

func linuxOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("io_uring backend is Linux-only")
	}
}

func TestDefaultConfigIsValid(t *testing.T) {
	linuxOnly(t)
	cfg := loopbackConfig()
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := srv.Config()

	if got.Loops != runtime.NumCPU() {
		t.Errorf("Loops = %d, want %d", got.Loops, runtime.NumCPU())
	}
	if got.MaxFrameSize > got.MaxMessageSize {
		t.Errorf("MaxFrameSize %d > MaxMessageSize %d", got.MaxFrameSize, got.MaxMessageSize)
	}
	if got.PingInterval >= got.IdleTimeout {
		t.Errorf("PingInterval %s must be below IdleTimeout %s", got.PingInterval, got.IdleTimeout)
	}
	if got.ReadBufferSize != 1024 {
		t.Errorf("ReadBufferSize = %d, want 1024", got.ReadBufferSize)
	}
	if got.BufferCount != 256 {
		t.Errorf("BufferCount = %d, want 256", got.BufferCount)
	}
	if got.TxBytes < got.MaxFrameSize+64 {
		t.Errorf("TxBytes %d cannot hold MaxFrameSize %d", got.TxBytes, got.MaxFrameSize)
	}
	if got.MaxConnections != 1_000_000 {
		t.Errorf("MaxConnections = %d, want 1,000,000", got.MaxConnections)
	}
}

func TestNewAllowsPlaintextOnAnyAddress(t *testing.T) {
	linuxOnly(t)
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0", ":8080", "0.0.0.0:8080"} {
		cfg := DefaultConfig()
		cfg.Addr = addr
		if _, err := New(cfg); err != nil {
			t.Errorf("New(%q): %v", addr, err)
		}
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	cfg := Config{
		Addr:               "",
		Loops:              -1,
		AcceptBacklog:      -5,
		MaxFrameSize:       1,
		MaxMessageSize:     0,
		MaxFragments:       0,
		MaxHeaderBytes:     1,
		HandshakeTimeout:   -time.Second,
		IdleTimeout:        0,
		PingInterval:       -1,
		CloseTimeout:       0,
		ShutdownTimeout:    0,
		WriteQueueBytes:    0,
		WriteQueueMessages: 0,
		Backpressure:       BackpressurePolicy(9),
		MaxRoomsPerConn:    -1,
		ReadBufferSize:     1,
		BufferCount:        -1,
		TxBytes:            -1,
		MaxConnections:     1<<24 + 1,
	}

	err := cfg.Validate()
	var cerr *ConfigError
	if !errors.As(err, &cerr) {
		t.Fatalf("error type = %T (%v), want *ConfigError", err, err)
	}
	if len(cerr.Problems) < 15 {
		t.Fatalf("collected %d problems, want >= 15 (Validate must not stop at the first)", len(cerr.Problems))
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"addr empty", func(c *Config) { c.Addr = " " }, "Addr is required"},
		{"addr no port", func(c *Config) { c.Addr = "localhost" }, "not host:port"},
		{"loops too many", func(c *Config) { c.Loops = 2048 }, "supported maximum"},
		{"frame below control size", func(c *Config) { c.MaxFrameSize = 100 }, "MaxFrameSize must be >= 125"},
		{"frame above message", func(c *Config) { c.MaxFrameSize = 9 << 20 }, "MaxFrameSize"},
		{"fragments zero", func(c *Config) { c.MaxFragments = -1 }, "MaxFragments"},
		{"header too small", func(c *Config) { c.MaxHeaderBytes = 10 }, "MaxHeaderBytes"},
		{"header too large", func(c *Config) { c.MaxHeaderBytes = 2 << 20 }, "MaxHeaderBytes"},
		{"idle timeout zero", func(c *Config) { c.IdleTimeout = -1 }, "IdleTimeout"},
		{"ping not below idle", func(c *Config) { c.PingInterval = c.IdleTimeout }, "must be less than IdleTimeout"},
		{"queue bytes zero", func(c *Config) { c.WriteQueueBytes = -1 }, "WriteQueueBytes"},
		{"bad origin", func(c *Config) { c.AllowedOrigins = []string{"app.example.com"} }, "AllowedOrigins"},
		{"bad origin scheme", func(c *Config) { c.AllowedOrigins = []string{"ftp://app.example.com"} }, "AllowedOrigins"},
		{"read buffer too small", func(c *Config) { c.ReadBufferSize = 10 }, "ReadBufferSize"},
		{"read buffer above frame", func(c *Config) { c.ReadBufferSize = 2 << 20 }, "ReadBufferSize"},
		{"buffer count zero", func(c *Config) { c.BufferCount = -1 }, "BufferCount"},
		{"tx bytes too small", func(c *Config) { c.TxBytes = 1024 }, "TxBytes"},
		{"too many connections", func(c *Config) { c.MaxConnections = 1<<24 + 1 }, "24-bit slot limit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loopbackConfig()
			tt.mutate(&cfg)
			cfg.normalize()

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestValidateAllowsWildcardOrigin(t *testing.T) {
	cfg := loopbackConfig()
	cfg.AllowedOrigins = []string{"*", "https://app.example.com", "http://localhost:3000"}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestZeroValueIsNotUsable(t *testing.T) {
	var cfg Config
	if err := cfg.Validate(); err == nil {
		t.Fatal("zero Config must not validate")
	}
}

func TestDefaultConfigHasNoWarnings(t *testing.T) {
	cfg := DefaultConfig()
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	_, warnings := cfg.validate()
	if len(warnings) != 0 {
		t.Fatalf("default warnings = %v", warnings)
	}
}

func TestConfigProblemIsTyped(t *testing.T) {
	cfg := loopbackConfig()
	cfg.MaxConnections = 1<<24 + 1
	cfg.normalize()
	err := cfg.Validate()
	var cerr *ConfigError
	if !errors.As(err, &cerr) || len(cerr.Problems) == 0 {
		t.Fatalf("err = %v", err)
	}
	found := false
	for _, p := range cerr.Problems {
		if p.Field == "MaxConnections" && p.Rule == "slot_limit" && p.Value != nil && p.Message != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("typed problem missing: %+v", cerr.Problems)
	}
}

func TestConfigWarnings(t *testing.T) {
	cfg := loopbackConfig()
	cfg.TokenVerifier = nil
	cfg.AllowedOrigins = []string{"*", "https://app.example.com", "https://app.example.com"}
	cfg.PingInterval = 0
	cfg.ChannelEvents = map[string]uint32{"orphan": 9999}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("warnings must not fail Validate: %v", err)
	}
	_, warnings := cfg.validate()
	rules := map[string]bool{}
	for _, w := range warnings {
		rules[w.Rule] = true
	}
	for _, want := range []string{"accept_all", "wildcard", "duplicate", "no_dead_peer_detection", "orphan_id"} {
		if !rules[want] {
			t.Fatalf("missing warning rule %q in %+v", want, warnings)
		}
	}
}
