# Vendored: giouring

`internal/uring/giouring` is a vendored copy of
[`github.com/pawelgaczynski/giouring`](https://github.com/pawelgaczynski/giouring)
at commit `69588b89acb9` (2023-08-26), MIT licensed. The upstream `LICENSE`
file is included.

## Why vendored

1. **Go 1.27 link failure.** Upstream uses `//go:linkname mmap syscall.mmap`
   and `//go:linkname munmap syscall.munmap`. The Go 1.27 toolchain no longer
   provides those link targets, so any binary importing the module fails at
   link time:

   ```
   link: github.com/pawelgaczynski/giouring: invalid reference to syscall.munmap
   ```

   `linked.go` is patched to call `unix.Syscall6(unix.SYS_MMAP, ...)` and
   `unix.Syscall(unix.SYS_MUNMAP, ...)` directly, preserving signatures and
   `MAP_FAILED` semantics.

2. **Pinning.** The backend's ABI assumptions (struct layouts, CQE flag
   positions, restriction encoding) are security-relevant. A vendored copy
   makes upgrades deliberate and reviewable.

3. **Linux-only.** Every file carries `//go:build linux`. `stub_other.go`
   keeps the package buildable (empty) on macOS so `go build ./...` and the
   portable tests still work.

## What was changed from upstream

| File | Change |
| ---- | ------ |
| all `.go` files | Added `//go:build linux` |
| `linked.go` | Replaced `go:linkname` mmap/munmap with public syscalls |
| `stub_other.go` | Added: non-Linux empty package |

No other source was modified.

## Updating

Do not run `go mod vendor` or `go get` for this package; it is not a module
dependency. To update, copy the new upstream source, re-apply the build tags
and the `linked.go` patch, and add the test binary to the kernel CI job.
