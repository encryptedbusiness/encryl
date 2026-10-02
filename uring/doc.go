// Package uring is the Linux io_uring toolkit used by the reactor.
//
// It wraps ring setup, completion tags, registration, probes, and the fixed
// RX/TX arenas. It is Linux-only. A ring is single-owner: one OS thread submits
// and drains completions.
//
//	ring, err := uring.NewRing(4096)
//	if err != nil { return err }
//	defer ring.QueueExit()
package uring
