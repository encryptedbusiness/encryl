//go:build linux

// Package testutil contains helpers shared by Linux-only test binaries.
package testutil

import "golang.org/x/sys/unix"

// RaiseMemlock raises the soft locked-memory limit to the hard limit.
// Newer kernels charge io_uring ring pages to this per-user quota, so a
// multi-loop or parallel test run can otherwise exhaust it and fail with
// ENOMEM. It is best effort: an unprivileged process cannot exceed the hard
// limit.
func RaiseMemlock() {
	var lim unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_MEMLOCK, &lim) != nil {
		return
	}
	if lim.Cur >= lim.Max {
		return
	}
	lim.Cur = lim.Max
	_ = unix.Setrlimit(unix.RLIMIT_MEMLOCK, &lim)
}
