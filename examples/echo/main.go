//go:build linux

// Command echo is the minimal M1 server used by the soak and Autobahn scripts.
// It echoes event 1024 back to the sender.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	loops := flag.Int("loops", 0, "event loops; 0 uses NumCPU")
	maxConns := flag.Int("max-conns", 1_000_000, "connection slab ceiling")
	readBuffer := flag.Int("read-buffer", 1024, "read buffer size")
	bufferCount := flag.Int("buffer-count", 256, "read buffers per loop")
	txBytes := flag.Int64("tx-bytes", (1<<20)+64, "TX arena bytes per loop")
	idleTimeout := flag.Duration("idle-timeout", 5*time.Minute, "idle connection timeout")
	heapProfile := flag.String("heap-profile", "/tmp/encryl-heap.prof", "SIGUSR2 heap profile path")
	flag.Parse()

	var srv *encryl.Server
	profiles := make(chan os.Signal, 1)
	signal.Notify(profiles, syscall.SIGUSR2)
	go func() {
		for range profiles {
			if srv != nil {
				log.Printf("stats: %+v", srv.Stats())
			}
			runtime.GC()
			f, err := os.Create(*heapProfile)
			if err != nil {
				log.Printf("heap profile: %v", err)
				continue
			}
			_ = pprof.WriteHeapProfile(f)
			_ = f.Close()
			log.Printf("heap profile written to %s", *heapProfile)
		}
	}()
	gcSignals := make(chan os.Signal, 1)
	signal.Notify(gcSignals, syscall.SIGUSR1)
	go func() {
		for range gcSignals {
			runtime.GC()
			log.Printf("forced GC")
		}
	}()
	statsSignals := make(chan os.Signal, 1)
	signal.Notify(statsSignals, syscall.SIGHUP)
	go func() {
		for range statsSignals {
			if srv != nil {
				log.Printf("stats: %+v", srv.Stats())
			}
		}
	}()

	cfg := encryl.DefaultConfig()
	cfg.Addr = *addr
	cfg.Loops = *loops
	cfg.MaxConnections = *maxConns
	cfg.ReadBufferSize = *readBuffer
	cfg.BufferCount = *bufferCount
	cfg.TxBytes = *txBytes
	cfg.AllowedOrigins = []string{"*"}
	cfg.IdleTimeout = *idleTimeout
	cfg.PingInterval = 30 * time.Second

	var err error
	srv, err = encryl.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *encryl.Conn, m encryl.Message) error {
		return c.EmitID(1024, m.Payload)
	}); err != nil {
		log.Fatal(err)
	}
	log.Printf("echo listening on %s", *addr)
	log.Fatal(srv.ListenAndServe(context.Background()))
}
