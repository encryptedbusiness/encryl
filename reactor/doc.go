// Package reactor is the single-owner io_uring event engine.
//
// One loop owns one ring and one connection slab. It accepts sockets, parses
// upgrades, drives receives and sends, and runs timers. Callers learn about
// work through callbacks; no callback runs on another loop's thread.
//
//	loop, err := reactor.NewLoop(cfg)
//	if err != nil { return err }
//	return loop.Run(ctx)
package reactor
