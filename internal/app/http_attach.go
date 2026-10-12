//go:build linux

package app

import (
	"net/http"
	"time"
	"unsafe"
)

// ServeHTTP implements http.Handler for the attached mode. The framework calls
// it after routing and authentication; it validates the handshake, hijacks the
// connection, and hands the descriptor to the reactor.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || !s.running.Load() || s.state.Load() == nil {
		http.Error(w, "encryl is not started", http.StatusServiceUnavailable)
		return
	}
	if r.TLS != nil {
		http.Error(w, "TLS must terminate upstream", http.StatusBadRequest)
		return
	}
	if r.ProtoMajor != 1 || r.ProtoMinor < 1 {
		http.Error(w, "HTTP/1.1 is required", http.StatusBadRequest)
		return
	}
	if r.URL == nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}

	h := upgradeHeadersFromNetHTTP(r)
	spec, err := s.PrepareUpgrade(h)
	if err != nil {
		ue, ok := err.(*UpgradeError)
		if !ok {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, ue.Reason, ue.Status)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}
	_ = conn.SetDeadline(time.Time{})

	var buffered []byte
	if n := rw.Reader.Buffered(); n > 0 {
		if b, err := rw.Reader.Peek(n); err == nil {
			buffered = make([]byte, len(b))
			copy(buffered, b)
		}
	}
	if err := s.Upgrade(conn, spec, buffered); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.Close()
}

func upgradeHeadersFromNetHTTP(r *http.Request) UpgradeHeaders {
	target := r.URL.RequestURI()
	if target == "" {
		target = "/"
	}
	return UpgradeHeaders{
		Method: byteView(r.Method),
		Proto:  byteView(r.Proto),
		Target: byteView(target),
		Host:   byteView(r.Host),

		HostCount:          boolCount(r.Host != ""),
		Origin:             headerFirst(r.Header, "Origin"),
		OriginCount:        headerCount(r.Header, "Origin"),
		Key:                headerFirst(r.Header, "Sec-Websocket-Key"),
		KeyCount:           headerCount(r.Header, "Sec-Websocket-Key"),
		Version:            headerFirst(r.Header, "Sec-Websocket-Version"),
		VersionCount:       headerCount(r.Header, "Sec-Websocket-Version"),
		Protocol:           headerFirst(r.Header, "Sec-Websocket-Protocol"),
		ProtocolCount:      headerCount(r.Header, "Sec-Websocket-Protocol"),
		ContentLength:      headerFirst(r.Header, "Content-Length"),
		ContentLengthCount: headerCount(r.Header, "Content-Length"),
		TransferEnc:        headerFirst(r.Header, "Transfer-Encoding"),
		TransferEncCount:   headerCount(r.Header, "Transfer-Encoding"),
		HasUpgrade:         headerHasToken(r.Header, "Upgrade", "websocket"),
		HasConnectionUp:    headerHasToken(r.Header, "Connection", "upgrade"),
		HasBody:            r.ContentLength != 0 || len(r.TransferEncoding) > 0,
	}
}

func byteView(s string) []byte {
	if s == "" {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}

func headerCount(h http.Header, key string) int {
	return len(h[key])
}

func headerFirst(h http.Header, key string) []byte {
	v := h[key]
	if len(v) == 0 {
		return nil
	}
	return byteView(v[0])
}

func headerHasToken(h http.Header, key, token string) bool {
	for _, v := range h[key] {
		if hasTokenString(v, token) {
			return true
		}
	}
	return false
}

func hasTokenString(value, want string) bool {
	for len(value) > 0 {
		i := 0
		for i < len(value) && value[i] != ',' {
			i++
		}
		token := trimStringOWS(value[:i])
		if equalFoldString(token, want) {
			return true
		}
		if i == len(value) {
			break
		}
		value = value[i+1:]
	}
	return false
}

func trimStringOWS(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func equalFoldString(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}
