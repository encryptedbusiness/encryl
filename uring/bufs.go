//go:build linux

package uring

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// RXArena is a per-loop read-buffer arena. A buffer is taken only when a
// connection is readable and an armed receive needs somewhere to land; an idle
// connection holds none.
//
// The buffer is selected in userspace and its address is passed to
// IORING_OP_RECV. This avoids the kernel provided-buffer group, whose
// re-publication with an explicit bid is rejected with EINVAL on the measured
// 6.8/arm64 host.
type RXArena struct {
	mem  []byte
	base uintptr
	size int
	n    int

	free []int32
	used []bool
}

func NewRXArena(size, count int) (*RXArena, error) {
	if size <= 0 || count <= 0 {
		return nil, fmt.Errorf("platform: invalid rx arena size=%d count=%d", size, count)
	}
	mem, err := unix.Mmap(-1, 0, size*count, unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANONYMOUS|unix.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("platform: mmap rx arena: %w", err)
	}
	a := &RXArena{
		mem:  mem,
		base: uintptr(unsafe.Pointer(&mem[0])),
		size: size,
		n:    count,
		free: make([]int32, count),
		used: make([]bool, count),
	}
	for i := range a.free {
		a.free[i] = int32(count - 1 - i)
	}
	return a, nil
}

// Size reports one buffer's size.
func (a *RXArena) Size() int { return a.size }

// Addr returns the kernel-visible address of one buffer.
func (a *RXArena) Addr(bid int32) uintptr {
	return a.base + uintptr(int(bid)*a.size)
}

// Bytes returns one buffer.
func (a *RXArena) Bytes(bid int32) []byte {
	off := int(bid) * a.size
	return a.mem[off : off+a.size]
}

// Take reserves one buffer.
func (a *RXArena) Take() (int32, bool) {
	if len(a.free) == 0 {
		return -1, false
	}
	n := len(a.free) - 1
	bid := a.free[n]
	a.free = a.free[:n]
	if int(bid) >= len(a.used) || a.used[bid] {
		return -1, false
	}
	a.used[bid] = true
	return bid, true
}

// Put returns one buffer.
func (a *RXArena) Put(bid int32) bool {
	if bid < 0 || int(bid) >= a.n || !a.used[bid] {
		return false
	}
	a.used[bid] = false
	a.free = append(a.free, bid)
	return true
}

// FreeCount reports the number of free buffers.
func (a *RXArena) FreeCount() int { return len(a.free) }

// InUse reports the number of reserved buffers.
func (a *RXArena) InUse() int { return a.n - len(a.free) }

// Balance reports the buffer accounting invariant.
func (a *RXArena) Balance() bool {
	return len(a.free)+a.InUse() == a.n && len(a.free) <= a.n
}

// Close unmaps the arena.
func (a *RXArena) Close() error {
	if a.mem == nil {
		return nil
	}
	err := unix.Munmap(a.mem)
	a.mem = nil
	a.base = 0
	return err
}

type txOwner struct {
	valid bool
	slot  uint32
	gen   uint32
}

// TXArena is a per-loop outbound block arena. A block exists only while a
// control frame (the 101 response, a close frame) is in flight.
type TXArena struct {
	mem   []byte
	base  uintptr
	size  int
	n     int
	owner []txOwner
	free  []int32
}

func NewTXArena(bytes int64, blockSize int) (*TXArena, error) {
	if bytes <= 0 || blockSize <= 0 {
		return nil, fmt.Errorf("platform: invalid tx arena bytes=%d block=%d", bytes, blockSize)
	}
	n := int(bytes) / blockSize
	if n < 1 {
		n = 1
	}
	mem, err := unix.Mmap(-1, 0, blockSize*n, unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANONYMOUS|unix.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("platform: mmap tx arena: %w", err)
	}
	a := &TXArena{
		mem:   mem,
		base:  uintptr(unsafe.Pointer(&mem[0])),
		size:  blockSize,
		n:     n,
		owner: make([]txOwner, n),
		free:  make([]int32, n),
	}
	for i := range a.free {
		a.free[i] = int32(i)
	}
	return a, nil
}

// Size reports one block's size.
func (a *TXArena) Size() int { return a.size }

// Addr returns the kernel-visible address of one block.
func (a *TXArena) Addr(bid int32) uintptr {
	return a.base + uintptr(int(bid)*a.size)
}

// Bytes returns one block.
func (a *TXArena) Bytes(bid int32) []byte {
	off := int(bid) * a.size
	return a.mem[off : off+a.size]
}

// Take reserves a block for a connection.
func (a *TXArena) Take(slot, gen uint32, n int) (int32, bool) {
	if n > a.size {
		return -1, false
	}
	if len(a.free) == 0 {
		return -1, false
	}
	bid := a.free[len(a.free)-1]
	a.free = a.free[:len(a.free)-1]
	a.owner[bid] = txOwner{valid: true, slot: slot, gen: gen}
	return bid, true
}

// Release returns a block, validating its owner.
func (a *TXArena) Release(bid int32, slot, gen uint32) bool {
	if bid < 0 || int(bid) >= a.n {
		return false
	}
	o := &a.owner[bid]
	if !o.valid || o.slot != slot || o.gen != gen {
		return false
	}
	o.valid = false
	a.free = append(a.free, bid)
	return true
}

// InUse reports the number of reserved blocks.
func (a *TXArena) InUse() int { return a.n - len(a.free) }

// Close unmaps the arena.
func (a *TXArena) Close() error {
	if a.mem == nil {
		return nil
	}
	err := unix.Munmap(a.mem)
	a.mem = nil
	a.base = 0
	return err
}
