// Package event implements the encryl.v2 envelope, registries, and ACK slab.
//
// The envelope is a fixed 16-byte header. Handler registration is frozen before
// traffic, so lookups are one atomic load and one map read.
//
//	env, err := event.Decode(raw)
//	payload := env.Payload
package event
