package room

import connection "github.com/encryptedbusiness/encryl/connslab"

// SelfRef resolves a public connection identity to its owning Ref. A
// Self-Room is implicit: it is never stored in the directory, so it costs zero
// bytes per connection. The owning loop validates the generation before the
// send, so a stale ID is rejected there rather than here.
func SelfRef(id connection.ID, perLoop uint32) (connection.Ref, bool) {
	if perLoop == 0 || id.Gen == 0 {
		return connection.Ref{}, false
	}
	loop := id.Slot / perLoop
	if loop >= 1<<16 {
		return connection.Ref{}, false
	}
	return connection.Ref{
		Loop: uint16(loop),
		Slot: id.Slot % perLoop,
		Gen:  id.Gen,
	}, true
}
