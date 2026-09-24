package room

import (
	"sync"

	connection "github.com/encryptedbusiness/encryl/connslab"
)

var roomScratchPool = sync.Pool{New: func() any {
	s := make([]*roomEntry, 0, 16)
	return &s
}}

// SnapshotInto appends the current members of a room to dst. It lets the
// fan-out planner reuse one scratch slice instead of allocating per Emit.
func (d *Directory) SnapshotInto(name string, dst []connection.Ref) []connection.Ref {
	entry, ok := d.hub(name).Load(name)
	if !ok {
		return dst
	}
	entry.mu.RLock()
	for ref := range entry.refs {
		dst = append(dst, ref)
	}
	entry.mu.RUnlock()
	return dst
}

// Size reports the number of members in a room without copying it.
func (d *Directory) Size(name string) int {
	entry, ok := d.hub(name).Load(name)
	if !ok {
		return 0
	}
	entry.mu.RLock()
	n := len(entry.refs)
	entry.mu.RUnlock()
	return n
}

// RoomNames iterates a connection's named room memberships. The names are
// copied under the lock and the callback runs after it is released, so a
// callback may call Join or Leave without deadlocking the directory. The
// Self-Room is implicit and is not stored, so it is not returned here.
func (d *Directory) RoomNames(ref connection.Ref, fn func(name string) bool) {
	p := roomScratchPool.Get().(*[]*roomEntry)
	rooms := (*p)[:0]

	d.mu.Lock()
	set := d.member(ref)
	rooms = append(rooms, set.rooms...)
	d.mu.Unlock()

	for _, entry := range rooms {
		if !fn(entry.name) {
			break
		}
	}

	*p = rooms[:0]
	roomScratchPool.Put(p)
}
