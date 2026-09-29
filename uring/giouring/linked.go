//go:build linux

// MIT License
//
// Copyright (c) 2023 Paweł Gaczyński
//
// Permission is hereby granted, free of charge, to any person obtaining a
// copy of this software and associated documentation files (the
// "Software"), to deal in the Software without restriction, including
// without limitation the rights to use, copy, modify, merge, publish,
// distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to
// the following conditions:
//
// The above copyright notice and this permission notice shall be included
// in all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS
// OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
// MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
// IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
// CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
// TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
// SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

package giouring

// LOCAL PATCH (Encryl v2, M0): upstream used
//
//	//go:linkname mmap syscall.mmap
//	//go:linkname munmap syscall.munmap
//
// Those symbols no longer link on Go 1.27: the toolchain removed the
// linkname-able wrappers from package syscall, so any binary importing this
// binding failed at link time with "invalid reference to syscall.munmap".
//
// The shims are reimplemented with public Syscall/Syscall6 calls, which are
// stable ABI on Linux. Signatures and MAP_FAILED semantics are unchanged.
//
// Keep this patch when updating the vendored copy. See NOTICE.md.

import (
	"golang.org/x/sys/unix"
)

func mmap(addr uintptr, length uintptr, prot int, flags int, fd int, offset int64) (xaddr uintptr, err error) {
	r, _, errno := unix.Syscall6(
		unix.SYS_MMAP,
		addr,
		length,
		uintptr(prot),
		uintptr(flags),
		uintptr(fd),
		uintptr(offset),
	)
	if errno != 0 {
		return ^uintptr(0), errno
	}
	return r, nil
}

func munmap(addr uintptr, length uintptr) (err error) {
	_, _, errno := unix.Syscall(unix.SYS_MUNMAP, addr, length, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
