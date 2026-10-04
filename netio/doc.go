// Package netio owns the listening socket and accepted-socket tuning.
//
// It is the "make our own listener" layer. The reactor accepts from the raw
// descriptor, so backlog and SO_REUSEPORT are explicit. It is Linux-only.
//
//	ln, err := netio.Listen(":8080", true, 4096)
//	if err != nil { return err }
//	defer ln.Close()
package netio
