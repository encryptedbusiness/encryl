//go:build linux

package reactor

// registerFile installs a process socket into a fixed-file table slot. The
// caller must close the process descriptor after this succeeds.
func (l *loop) registerFile(slot uint32, fd int) bool {
	if l.ring == nil {
		l.c.fileTableErrs.Add(1)
		return false
	}
	if _, err := l.ring.RegisterFile(uint(slot), fd); err != nil {
		l.c.fileTableErrs.Add(1)
		l.logf("file table update slot=%d: %v", slot, err)
		return false
	}
	return true
}

// clearFile removes a fixed-file table entry. It is used only on the forced
// shutdown path, where a normal close completion must not be awaited.
func (l *loop) clearFile(slot uint32) {
	if l.ring == nil {
		return
	}
	if _, err := l.ring.RegisterFile(uint(slot), -1); err != nil {
		l.c.fileTableErrs.Add(1)
		l.logf("file table clear slot=%d: %v", slot, err)
	}
}
