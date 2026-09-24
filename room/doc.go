// Package room owns room membership and fan-out planning.
//
// Room lookups use a lock-free concurrent map; each room has its own lock for
// membership. Fan-out groups references by owning loop before dispatch.
//
//	d := room.New(128)
//	_ = d.Join("general", ref)
//	refs := d.SnapshotInto("general", nil)
package room
