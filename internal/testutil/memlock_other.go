//go:build !linux

package testutil

// RaiseMemlock is a no-op on platforms without io_uring.
func RaiseMemlock() {}
