// Package protocol implements the optional protobuf event protocol.
//
// Messages are varint-length-prefixed protobuf batches inside one WebSocket
// binary frame. The event core, rooms, and Self-Rooms stay shared with the
// native protocol.
//
//	cmds, err := protocol.DecodeCommands(payload)
package protocol
