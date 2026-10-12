//go:build linux

package app

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
)

// UpgradeError reports a rejected WebSocket upgrade with the HTTP status the
// framework should send.
type UpgradeError struct {
	Status int
	Reason string
}

func (e *UpgradeError) Error() string { return e.Reason }

// UpgradeHeaders is the already-parsed handshake data from a framework. Every
// []byte field is borrowed and is only valid for the PrepareUpgrade call.
type UpgradeHeaders struct {
	Method, Proto, Target []byte
	Host, Origin          []byte
	Key, Version          []byte
	Protocol              []byte
	ContentLength         []byte
	TransferEnc           []byte

	HostCount, OriginCount, KeyCount, VersionCount int
	ProtocolCount, ContentLengthCount              int
	TransferEncCount                               int
	HasUpgrade, HasConnectionUp, HasBody           bool
}

// UpgradeSpec is the small, allocation-free result of validating an upgrade.
type UpgradeSpec struct {
	Accept         [28]byte
	Subprotocol    [32]byte
	SubprotocolLen uint8
	Encoding       uint8
	prepared       bool
}

// PrepareUpgrade validates the framework-parsed handshake and computes the 101
// accept value. It does not read the socket and does not synthesize an HTTP
// request.
func (s *Server) PrepareUpgrade(h UpgradeHeaders) (UpgradeSpec, error) {
	var out UpgradeSpec
	if s == nil {
		return out, &UpgradeError{Status: 500, Reason: "server is nil"}
	}
	if !bytes.Equal(h.Method, []byte("GET")) {
		return out, &UpgradeError{Status: 405, Reason: "method must be GET"}
	}
	if !bytes.Equal(h.Proto, []byte("HTTP/1.1")) {
		return out, &UpgradeError{Status: 400, Reason: "HTTP/1.1 is required"}
	}
	if len(h.Target) == 0 || h.Target[0] != '/' {
		return out, &UpgradeError{Status: 400, Reason: "request target must be an absolute path"}
	}
	if h.HostCount != 1 || len(h.Host) == 0 {
		return out, &UpgradeError{Status: 400, Reason: "missing required WebSocket headers"}
	}
	if !h.HasUpgrade || !h.HasConnectionUp || h.VersionCount != 1 || h.KeyCount != 1 {
		return out, &UpgradeError{Status: 400, Reason: "missing required WebSocket headers"}
	}
	if !bytes.Equal(h.Version, []byte("13")) {
		return out, &UpgradeError{Status: 426, Reason: "Sec-WebSocket-Version must be 13"}
	}
	if len(h.Key) != 24 {
		return out, &UpgradeError{Status: 400, Reason: "invalid Sec-WebSocket-Key"}
	}
	if !acceptKey(h.Key, &out.Accept) {
		return out, &UpgradeError{Status: 400, Reason: "invalid Sec-WebSocket-Key"}
	}
	if h.OriginCount > 1 {
		return out, &UpgradeError{Status: 400, Reason: "duplicate Origin"}
	}
	if h.ProtocolCount > 1 {
		return out, &UpgradeError{Status: 400, Reason: "duplicate Sec-WebSocket-Protocol"}
	}
	if h.ContentLengthCount > 1 || h.TransferEncCount > 1 {
		return out, &UpgradeError{Status: 400, Reason: "request body is not allowed"}
	}
	if h.HasBody || len(h.TransferEnc) > 0 {
		return out, &UpgradeError{Status: 400, Reason: "request body is not allowed"}
	}
	if len(h.ContentLength) > 0 && !bytes.Equal(trimOWS(h.ContentLength), []byte("0")) {
		return out, &UpgradeError{Status: 400, Reason: "request body is not allowed"}
	}

	var (
		formatPB  bool
		badFormat bool
		subEnc    uint8
		subLen    int
	)
	parseProtocolQuery(h.Target, &formatPB, &badFormat)
	if h.ProtocolCount > 0 {
		selected := false
		forEachToken(h.Protocol, func(token []byte) {
			if selected {
				return
			}
			switch {
			case bytes.EqualFold(token, []byte("encryl.pb.v1")):
				subEnc, selected = 1, true
			case bytes.EqualFold(token, []byte("encryl.v2")):
				subEnc, selected = 0, true
			}
			if selected && len(token) <= len(out.Subprotocol) {
				copy(out.Subprotocol[:], token)
				subLen = len(token)
			}
		})
		if !selected {
			return out, &UpgradeError{Status: 400, Reason: "Sec-WebSocket-Protocol must include encryl.v2 or encryl.pb.v1"}
		}
	}
	out.SubprotocolLen = uint8(subLen)
	if badFormat {
		return out, &UpgradeError{Status: 400, Reason: "unsupported protocol format"}
	}
	if h.ProtocolCount > 0 && !formatPB && subEnc == 1 {
		out.Encoding = 1
	} else if h.ProtocolCount > 0 && subEnc == 0 {
		out.Encoding = 0
	} else {
		out.Encoding = 1
	}
	if formatPB && h.ProtocolCount > 0 && subEnc == 0 {
		return out, &UpgradeError{Status: 400, Reason: "protobuf format conflicts with encryl.v2 subprotocol"}
	}
	if formatPB {
		out.Encoding = 1
	}

	if h.OriginCount == 1 && len(h.Origin) > 0 && !s.originAllowedBytes(h.Origin, h.Host) {
		return out, &UpgradeError{Status: 403, Reason: "origin not allowed"}
	}
	out.prepared = true
	return out, nil
}

func (s *Server) originAllowedBytes(origin, host []byte) bool {
	if len(s.cfg.AllowedOrigins) == 0 {
		oh, ok := originHost(origin)
		return ok && bytes.EqualFold(oh, host)
	}
	for _, allowed := range s.cfg.AllowedOrigins {
		if allowed == "*" || equalFoldStringBytes(allowed, origin) {
			return true
		}
	}
	return false
}

func originHost(origin []byte) ([]byte, bool) {
	i := bytes.Index(origin, []byte("://"))
	if i <= 0 {
		return nil, false
	}
	rest := origin[i+3:]
	if len(rest) == 0 {
		return nil, false
	}
	end := len(rest)
	for j, b := range rest {
		if b == '/' || b == '?' || b == '#' {
			end = j
			break
		}
	}
	if end == 0 {
		return nil, false
	}
	return rest[:end], true
}

func equalFoldStringBytes(s string, b []byte) bool {
	if len(s) != len(b) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if lowerASCII(s[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func parseProtocolQuery(target []byte, formatPB, badFormat *bool) {
	i := bytes.IndexByte(target, '?')
	if i < 0 {
		return
	}
	q := target[i+1:]
	for len(q) > 0 {
		var part []byte
		if j := bytes.IndexByte(q, '&'); j >= 0 {
			part, q = q[:j], q[j+1:]
		} else {
			part, q = q, nil
		}
		j := bytes.IndexByte(part, '=')
		if j <= 0 {
			continue
		}
		key := trimOWS(part[:j])
		val := trimOWS(part[j+1:])
		switch {
		case bytes.EqualFold(key, []byte("format")), bytes.EqualFold(key, []byte("protocol")):
			switch {
			case bytes.EqualFold(val, []byte("protobuf")), bytes.EqualFold(val, []byte("pb")):
				*formatPB = true
			case bytes.EqualFold(val, []byte("json")):
				*badFormat = true
			default:
				*badFormat = true
			}
		}
	}
}

func forEachToken(value []byte, fn func([]byte)) {
	for len(value) > 0 {
		var token []byte
		if i := bytes.IndexByte(value, ','); i >= 0 {
			token, value = value[:i], value[i+1:]
		} else {
			token, value = value, nil
		}
		token = trimOWS(token)
		if len(token) > 0 {
			fn(token)
		}
	}
}

func trimOWS(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

func acceptKey(key []byte, out *[28]byte) bool {
	if len(key) != 24 {
		return false
	}
	var raw [16]byte
	n, err := base64.StdEncoding.Decode(raw[:], key)
	if err != nil || n != 16 {
		return false
	}
	var buf [60]byte
	copy(buf[:24], key)
	copy(buf[24:], webSocketGUID)
	sum := sha1.Sum(buf[:])
	base64.StdEncoding.Encode(out[:], sum[:])
	return true
}

const webSocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
