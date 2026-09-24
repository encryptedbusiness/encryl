// Package room implements the room directory used by Emit.
package room

import (
	"errors"
	"sync"

	connection "github.com/encryptedbusiness/encryl/connslab"
	"github.com/encryptedbusiness/encryl/internal/fnv"
	"github.com/puzpuzpuz/xsync/v4"
)

// ErrLimit is returned when a connection would exceed MaxRoomsPerConn.
var ErrLimit = errors.New("room: membership limit reached")

// Ref identifies a connection on a specific loop.
type Ref = connection.Ref

type roomEntry struct {
	name string
	mu   sync.RWMutex
	refs map[Ref]struct{}
}

const hubCount = 4

// connRooms is the compact per-connection membership record. Most connections
// join one or two rooms; a slice is much smaller than a map.
type connRooms struct {
	rooms []*roomEntry
}

// Directory owns room membership. The room map is lock-free for reads; each
// room entry has its own lock, so unrelated rooms do not contend.
type Directory struct {
	hubs []*xsync.Map[string, *roomEntry]

	mu           sync.Mutex
	members      map[Ref]connRooms
	slots        []connRooms
	slotCapacity int
	perLoop      uint32
	memberships  int
	maxPerConn   int
}

// New returns a directory. maxPerConn <= 0 disables the per-connection limit.
func newHubs(n int) []*xsync.Map[string, *roomEntry] {
	if n < 1 {
		n = 1
	}
	hubs := make([]*xsync.Map[string, *roomEntry], n)
	for i := range hubs {
		hubs[i] = xsync.NewMap[string, *roomEntry]()
	}
	return hubs
}

func New(maxPerConn int) *Directory {
	return &Directory{
		hubs:       newHubs(hubCount),
		members:    make(map[Ref]connRooms),
		maxPerConn: maxPerConn,
	}
}

// NewSized uses a dense per-slot membership table. It is the production
// constructor: one slice header per connection is smaller than a map entry.
func NewSized(maxPerConn int, perLoop uint32, maxSlots int) *Directory {
	if perLoop == 0 || maxSlots <= 0 {
		return New(maxPerConn)
	}
	return &Directory{
		hubs:         newHubs(hubCount),
		members:      make(map[Ref]connRooms),
		slotCapacity: maxSlots,
		perLoop:      perLoop,
		maxPerConn:   maxPerConn,
	}
}

func (d *Directory) hub(name string) *xsync.Map[string, *roomEntry] {
	if len(d.hubs) == 1 {
		return d.hubs[0]
	}
	return d.hubs[int(fnv.Hash32(name))%len(d.hubs)]
}

func (d *Directory) slotIndex(ref Ref) (int, bool) {
	if d.slots == nil || d.perLoop == 0 {
		return 0, false
	}
	idx := int(ref.Loop)*int(d.perLoop) + int(ref.Slot)
	return idx, idx >= 0 && idx < len(d.slots)
}

func (d *Directory) ensureSlots() {
	if d.slots == nil && d.slotCapacity > 0 {
		d.slots = make([]connRooms, d.slotCapacity)
	}
}

func (d *Directory) member(ref Ref) connRooms {
	if idx, ok := d.slotIndex(ref); ok {
		return d.slots[idx]
	}
	return d.members[ref]
}

func (d *Directory) setMember(ref Ref, set connRooms) {
	d.ensureSlots()
	if idx, ok := d.slotIndex(ref); ok {
		d.slots[idx] = set
		return
	}
	d.members[ref] = set
}

func (d *Directory) deleteMember(ref Ref) {
	if idx, ok := d.slotIndex(ref); ok {
		d.slots[idx] = connRooms{}
		return
	}
	delete(d.members, ref)
}

func (d *Directory) room(name string) *roomEntry {
	hub := d.hub(name)
	if entry, ok := hub.Load(name); ok {
		return entry
	}
	created := &roomEntry{name: name, refs: make(map[Ref]struct{}, 4)}
	entry, _ := hub.LoadOrStore(name, created)
	return entry
}

func containsRoom(rooms []*roomEntry, want *roomEntry) bool {
	for _, room := range rooms {
		if room == want {
			return true
		}
	}
	return false
}

// Join adds a connection to a room. Joining twice is not an error.
func (d *Directory) Join(name string, ref Ref) error {
	entry := d.room(name)

	d.mu.Lock()
	set := d.member(ref)
	if containsRoom(set.rooms, entry) {
		d.mu.Unlock()
		return nil
	}
	if d.maxPerConn > 0 && len(set.rooms) >= d.maxPerConn {
		d.mu.Unlock()
		return ErrLimit
	}
	set.rooms = append(set.rooms, entry)
	d.setMember(ref, set)
	d.memberships++
	d.mu.Unlock()

	entry.mu.Lock()
	entry.refs[ref] = struct{}{}
	entry.mu.Unlock()
	return nil
}

// Leave removes a connection from a room. Leaving a room it is not in is not
// an error.
func (d *Directory) Leave(name string, ref Ref) {
	entry, ok := d.hub(name).Load(name)
	if !ok {
		return
	}
	d.mu.Lock()
	set := d.member(ref)
	idx := -1
	for i, member := range set.rooms {
		if member == entry {
			idx = i
			break
		}
	}
	if idx < 0 {
		d.mu.Unlock()
		return
	}
	set.rooms = append(set.rooms[:idx], set.rooms[idx+1:]...)
	if len(set.rooms) == 0 {
		d.deleteMember(ref)
	} else {
		d.setMember(ref, set)
	}
	d.memberships--
	d.mu.Unlock()

	entry.mu.Lock()
	delete(entry.refs, ref)
	entry.mu.Unlock()
}

// RemoveConn removes every membership for a connection. It is called from the
// owning loop's close path.
func (d *Directory) RemoveConn(ref Ref) {
	d.mu.Lock()
	set := d.member(ref)
	if len(set.rooms) == 0 {
		d.mu.Unlock()
		return
	}
	rooms := append([]*roomEntry(nil), set.rooms...)
	d.deleteMember(ref)
	d.memberships -= len(rooms)
	d.mu.Unlock()

	for _, entry := range rooms {
		entry.mu.Lock()
		delete(entry.refs, ref)
		entry.mu.Unlock()
	}
}

// Counts returns the number of non-empty rooms and total memberships.
func (d *Directory) Counts() (rooms, memberships int) {
	d.mu.Lock()
	memberships = d.memberships
	d.mu.Unlock()

	for _, hub := range d.hubs {
		hub.Range(func(_ string, entry *roomEntry) bool {
			entry.mu.RLock()
			if len(entry.refs) != 0 {
				rooms++
			}
			entry.mu.RUnlock()
			return true
		})
	}
	return rooms, memberships
}
