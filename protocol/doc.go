// Package protocol implements the encryl event protocol.
//
// Bebop is the only client event codec. It uses varint-length-prefixed batches
// inside one WebSocket binary frame. The event core, rooms, and Self-Rooms stay
// shared with the native protocol.
//
//	cmds, err := protocol.DecodeBebopCommands(payload)
package protocol
