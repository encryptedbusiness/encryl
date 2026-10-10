// Package encryl is a WebSocket server built for very high connection
// counts with bounded memory per connection.
//
// # Status
//
// This is the v1 line. The public surface is a thin facade over four vertical
// slices: event, room, connection, and transport, on a shared kernel of
// reactor, platform, and timerwheel. See docs/architecture.md and
// docs/adr/0014-vertical-slices.md.
//
// The server is Linux-only and uses io_uring exclusively. A non-Linux build
// compiles so the portable slices stay testable, but New panics with a message
// that names the host.
//
// # Design commitments
//
//   - No public maps, channels, file descriptors, or mutable internals. A Conn
//     is an opaque handle validated by slot and generation on every call.
//   - Every send goes through a room. A direct send is an Emit to the
//     connection's implicit Self-Room, which costs zero storage.
//   - Emit reports admission synchronously. A full write queue returns
//     ErrBackpressure; it never blocks the caller and never silently drops.
//   - Payloads passed to handlers are borrowed and valid only for the duration
//     of the call.
//   - Shutdown cancels in-flight work, drains within a deadline, closes every
//     descriptor exactly once, and joins every loop.
//   - The library speaks plaintext ws:// only. TLS terminates at a load
//     balancer or reverse proxy in front of the process.
//
// # Minimum example
//
//	cfg := encryl.DefaultConfig()
//	cfg.Addr = ":8080"
//
//	srv, err := encryl.New(cfg)
//	if err != nil {
//		log.Fatal(err)
//	}
//	srv.On(chatEvent, func(c *encryl.Conn, m encryl.Message) error {
//		return c.To("general").Emit(chatEvent, m.Payload)
//	})
//	log.Fatal(srv.ListenAndServe(context.Background()))
package encryl
