//go:build linux

// Command fiber demonstrates encryl behind a Fiber v3 application.
//
// Fiber owns the listener. The /ws route hands the fasthttp upgrade to
// encryl through the adapter; encryl writes the 101.
//
//	cd examples/fiber
//	go run .
//	websocat ws://127.0.0.1:18091/ws
//
// On Linux only; the reactor is io_uring-only.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl"
	fiberadapter "github.com/encryptedbusiness/encryl/adapters/fiberv3"
	"github.com/gofiber/fiber/v3"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18091", "HTTP listen address")
	loops := flag.Int("loops", 0, "event loops; 0 uses NumCPU")
	flag.Parse()

	cfg := encryl.DefaultConfig()
	cfg.Loops = *loops
	cfg.MaxConnections = 100_000
	cfg.AllowedOrigins = []string{"*"}

	srv, err := encryl.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.OnConnect(func(c *encryl.Conn) error {
		return c.Join("lobby")
	}); err != nil {
		log.Fatal(err)
	}
	if err := srv.On("echo", func(c *encryl.Conn, data []byte) {
		_ = c.Emit("echo", data)
	}); err != nil {
		log.Fatal(err)
	}
	if err := srv.On("broadcast", func(c *encryl.Conn, data []byte) {
		_ = c.To("lobby").Emit("broadcast", data)
	}); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Start(ctx); err != nil {
		log.Fatal(err)
	}

	app := fiber.New()
	app.Get("/healthz", func(c fiber.Ctx) error {
		return c.SendString("ok\n")
	})
	app.Get("/ws", fiberadapter.Handler(srv))

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("Fiber example: HTTP http://%s/  WebSocket ws://%s/ws", *addr, *addr)
		if err := app.Listener(ln); err != nil && !errors.Is(err, net.ErrClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			log.Printf("Fiber server: %v", err)
		}
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = app.ShutdownWithContext(shutdown)
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("encryl shutdown: %v", err)
	}
}
