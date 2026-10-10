//go:build linux

package app

import "net"

// Addr returns the bound address of the first loop's listener. It is nil
// before the server starts and in attached mode, where the framework owns the
// listener.
func (s *Server) Addr() net.Addr {
	if s == nil {
		return nil
	}
	st := s.state.Load()
	if st == nil || len(st.loops) == 0 {
		return nil
	}
	return st.loops[0].Addr()
}
