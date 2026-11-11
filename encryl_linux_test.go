//go:build linux

package encryl

import (
	"errors"
	"runtime"
	"testing"

	"github.com/encryptedbusiness/encryl/internal/app"
	"github.com/encryptedbusiness/encryl/internal/fnv"
)

func TestEventIDForNameMatchesWireHash(t *testing.T) {
	names := []string{"chat", "chat.message", "room:presence:update", ""}
	for _, name := range names {
		if got, want := EventIDForName(name), fnv.Hash32(name); got != want {
			t.Errorf("EventIDForName(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestFacadeAliasesMatchImplementation(t *testing.T) {
	if !errors.Is(ErrClosed, app.ErrClosed) ||
		!errors.Is(ErrBackpressure, app.ErrBackpressure) ||
		!errors.Is(ErrSlowConsumer, app.ErrSlowConsumer) ||
		!errors.Is(ErrPayloadTooLarge, app.ErrPayloadTooLarge) ||
		!errors.Is(ErrUnauthorized, app.ErrUnauthorized) ||
		!errors.Is(ErrRoomLimit, app.ErrRoomLimit) ||
		!errors.Is(ErrUnsupported, app.ErrUnsupported) ||
		!errors.Is(ErrAckTimeout, app.ErrAckTimeout) ||
		!errors.Is(ErrInvalidUpgrade, app.ErrInvalidUpgrade) {
		t.Fatal("a facade error is not the implementation sentinel")
	}

	if BackpressureReject != app.BackpressureReject ||
		BackpressureDropNewest != app.BackpressureDropNewest ||
		BackpressureDropOldest != app.BackpressureDropOldest ||
		BackpressureCloseSlow != app.BackpressureCloseSlow {
		t.Fatal("a facade backpressure constant drifted")
	}
}

func TestDefaultConfigAndNew(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Addr != ":8080" {
		t.Fatalf("Addr = %q", cfg.Addr)
	}
	if cfg.MaxConnections <= 0 || cfg.MaxFrameSize <= 0 {
		t.Fatalf("defaults = %+v", cfg)
	}

	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv == nil {
		t.Fatal("New returned a nil server")
	}
	if got := srv.Config(); got.Loops != runtime.NumCPU() {
		t.Fatalf("Loops = %d, want %d", got.Loops, runtime.NumCPU())
	}
}

func TestNewCollectsConfigErrors(t *testing.T) {
	var cfg Config
	if err := cfg.Validate(); err == nil {
		t.Fatal("zero config validated")
	} else {
		var ce *ConfigError
		if !errors.As(err, &ce) || len(ce.Problems) == 0 {
			t.Fatalf("error = %T (%v)", err, err)
		}
	}

	if _, err := New(Config{}); err == nil {
		t.Fatal("New accepted a zero config")
	}
}
