// Package upgrade implements a strict, incremental HTTP/1.1 WebSocket upgrade
// parser. It keeps only the fields it needs, bounds the request by
// MaxHeaderBytes and the header count, and never buffers the whole request.
package httpx

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"strconv"
)

// Result is the outcome of a Feed call.
type Result uint8

const (
	// NeedMore means the request is incomplete. Feed again with more bytes.
	NeedMore Result = iota
	// Complete means the headers ended and the upgrade is valid. The returned
	// consumed count points just past the terminating blank line.
	Complete
	// Rejected means the request is invalid.
	Rejected
)

// Limits bounds the upgrade request.
type Limits struct {
	MaxHeaderBytes int
	MaxHeaders     int
}

// DefaultLimits matches Config's defaults.
func DefaultLimits() Limits {
	return Limits{MaxHeaderBytes: 8 << 10, MaxHeaders: 100}
}

// RejectError describes why an upgrade was rejected.
type RejectError struct {
	Status int
	Reason string
}

func (e *RejectError) Error() string { return e.Reason }

// Request carries the values the reactor needs after a successful parse. Host
// and Origin are views into parser-owned buffers and are valid until Reset.
type Request struct {
	Accept    [28]byte
	Host      []byte
	Origin    []byte
	HasOrigin bool
	// Encoding is 1 for Bebop, 0 for the native envelope.
	Encoding uint8
	// Subprotocol is empty for a protocol connection that negotiated with a
	// query parameter only.
	Subprotocol []byte
}

// Parser is an incremental upgrade parser. It is not safe for concurrent use.
type Parser struct {
	limits Limits

	line        []byte
	sawCR       bool
	total       int
	headers     int
	request     bool
	done        bool
	hasHost     bool
	hasUp       bool
	hasConn     bool
	hasVer      bool
	hasKey      bool
	hasProto    bool
	subEncoding int
	formatBebop bool
	badFormat   bool

	host   []byte
	origin []byte
	sub    []byte

	accept [28]byte
}

// NewParser returns a parser with the given limits.
func NewParser(limits Limits) *Parser {
	p := &Parser{}
	p.Init(limits)
	return p
}

// Init resets the parser and applies limits. Zero limits fall back to defaults.
func (p *Parser) Init(limits Limits) {
	if limits.MaxHeaderBytes <= 0 {
		limits.MaxHeaderBytes = 8 << 10
	}
	if limits.MaxHeaders <= 0 {
		limits.MaxHeaders = 100
	}
	p.limits = limits
	p.Reset()
}

// Reset returns the parser to its initial state. It releases the line, host,
// and origin buffers.
func (p *Parser) Reset() {
	// Keep small buffers so a pooled parser does not allocate once per
	// handshake. Large outliers are released.
	p.line = reuseBuf(p.line, 4096)
	p.host = reuseBuf(p.host, 1024)
	p.origin = reuseBuf(p.origin, 1024)
	p.sawCR = false
	p.total = 0
	p.headers = 0
	p.request = false
	p.done = false
	p.hasHost = false
	p.hasUp = false
	p.hasConn = false
	p.hasVer = false
	p.hasKey = false
	p.hasProto = false
	p.subEncoding = -1
	p.formatBebop = false
	p.badFormat = false
	p.sub = p.sub[:0]
	p.accept = [28]byte{}
}

func reuseBuf(b []byte, limit int) []byte {
	if cap(b) > limit {
		return nil
	}
	return b[:0]
}

// Done reports whether the parser completed successfully.
func (p *Parser) Done() bool { return p.done }

// Request returns the parsed request. It is valid after Complete and until
// Reset.
func (p *Parser) Request() Request {
	enc := p.subEncoding
	switch {
	case p.formatBebop:
		enc = 1
	case !p.hasProto:
		enc = 1
	}
	if enc < 0 {
		enc = 1
	}
	return Request{
		Accept:      p.accept,
		Host:        p.host,
		Origin:      p.origin,
		HasOrigin:   len(p.origin) > 0,
		Encoding:    uint8(enc),
		Subprotocol: p.sub,
	}
}

// Feed consumes bytes until the request is complete, rejected, or exhausted.
// consumed is the number of bytes accounted for; when Complete, the caller
// should feed data[consumed:] to the WebSocket parser.
func (p *Parser) Feed(data []byte) (Result, int, error) {
	for i, b := range data {
		p.total++
		if p.total > p.limits.MaxHeaderBytes {
			return Rejected, i + 1, &RejectError{Status: 431, Reason: "request headers too large"}
		}

		switch b {
		case '\r':
			if p.sawCR {
				return Rejected, i + 1, &RejectError{Status: 400, Reason: "bare carriage return"}
			}
			p.sawCR = true
			continue
		case '\n':
			if !p.sawCR {
				return Rejected, i + 1, &RejectError{Status: 400, Reason: "bare line feed"}
			}
			p.sawCR = false
			if err := p.processLine(); err != nil {
				return Rejected, i + 1, err
			}
			p.line = p.line[:0]
			if p.done {
				return Complete, i + 1, nil
			}
			continue
		default:
			if p.sawCR {
				return Rejected, i + 1, &RejectError{Status: 400, Reason: "carriage return not followed by line feed"}
			}
			p.line = append(p.line, b)
			if len(p.line) > p.limits.MaxHeaderBytes {
				return Rejected, i + 1, &RejectError{Status: 431, Reason: "header line too large"}
			}
		}
	}
	return NeedMore, len(data), nil
}

func (p *Parser) processLine() error {
	if !p.request {
		return p.parseRequestLine()
	}
	if len(p.line) == 0 {
		return p.finish()
	}
	return p.parseHeader()
}

func (p *Parser) parseRequestLine() error {
	line := p.line
	i := bytes.IndexByte(line, ' ')
	if i <= 0 {
		return &RejectError{Status: 400, Reason: "malformed request line"}
	}
	j := bytes.IndexByte(line[i+1:], ' ')
	if j < 0 {
		return &RejectError{Status: 400, Reason: "malformed request line"}
	}
	j += i + 1
	method := line[:i]
	target := line[i+1 : j]
	version := line[j+1:]

	if !bytes.Equal(method, []byte("GET")) {
		return &RejectError{Status: 405, Reason: "method must be GET"}
	}
	if !bytes.Equal(version, []byte("HTTP/1.1")) {
		return &RejectError{Status: 400, Reason: "HTTP/1.1 is required"}
	}
	if len(target) == 0 || target[0] != '/' {
		return &RejectError{Status: 400, Reason: "request target must be an absolute path"}
	}
	if i := bytes.IndexByte(target, '?'); i >= 0 {
		p.parseQuery(target[i+1:])
	}
	p.request = true
	return nil
}

// parseQuery reads the protocol hints from the request query. The only
// accepted format is ?format=bebop or ?protocol=bebop.
func (p *Parser) parseQuery(q []byte) {
	for len(q) > 0 {
		var part []byte
		if i := bytes.IndexByte(q, '&'); i >= 0 {
			part, q = q[:i], q[i+1:]
		} else {
			part, q = q, nil
		}
		i := bytes.IndexByte(part, '=')
		if i <= 0 {
			continue
		}
		key := trim(part[:i])
		val := trim(part[i+1:])
		switch {
		case bytes.EqualFold(key, []byte("format")), bytes.EqualFold(key, []byte("protocol")):
			switch {
			case bytes.EqualFold(val, []byte("bebop")), bytes.EqualFold(val, []byte("bb")):
				p.formatBebop = true
			default:
				p.badFormat = true
			}
		}
	}
}

func (p *Parser) parseHeader() error {
	line := p.line
	i := bytes.IndexByte(line, ':')
	if i <= 0 {
		return &RejectError{Status: 400, Reason: "malformed header"}
	}
	name := trim(line[:i])
	value := trim(line[i+1:])
	if len(name) == 0 || !validToken(name) {
		return &RejectError{Status: 400, Reason: "invalid header name"}
	}
	if !validValue(value) {
		return &RejectError{Status: 400, Reason: "invalid header value"}
	}
	p.headers++
	if p.headers > p.limits.MaxHeaders {
		return &RejectError{Status: 431, Reason: "too many headers"}
	}

	switch {
	case bytes.EqualFold(name, []byte("host")):
		if p.hasHost {
			return &RejectError{Status: 400, Reason: "duplicate Host"}
		}
		if len(value) == 0 {
			return &RejectError{Status: 400, Reason: "empty Host"}
		}
		p.hasHost = true
		p.host = append(p.host[:0], value...)
	case bytes.EqualFold(name, []byte("upgrade")):
		if containsToken(value, []byte("websocket")) {
			p.hasUp = true
		}
	case bytes.EqualFold(name, []byte("connection")):
		if containsToken(value, []byte("upgrade")) {
			p.hasConn = true
		}
	case bytes.EqualFold(name, []byte("sec-websocket-version")):
		if p.hasVer {
			return &RejectError{Status: 400, Reason: "duplicate Sec-WebSocket-Version"}
		}
		p.hasVer = true
		if !bytes.Equal(value, []byte("13")) {
			return &RejectError{Status: 426, Reason: "Sec-WebSocket-Version must be 13"}
		}
	case bytes.EqualFold(name, []byte("sec-websocket-key")):
		if p.hasKey {
			return &RejectError{Status: 400, Reason: "duplicate Sec-WebSocket-Key"}
		}
		p.hasKey = true
		if !p.acceptKey(value) {
			return &RejectError{Status: 400, Reason: "invalid Sec-WebSocket-Key"}
		}
	case bytes.EqualFold(name, []byte("sec-websocket-protocol")):
		if p.hasProto {
			return &RejectError{Status: 400, Reason: "duplicate Sec-WebSocket-Protocol"}
		}
		p.hasProto = true
		p.sub = p.sub[:0]
		p.subEncoding = -1
		for _, token := range splitTokens(value) {
			enc := -1
			switch {
			case bytes.EqualFold(token, []byte("encryl.bebop.v1")):
				enc = 1
			case bytes.EqualFold(token, []byte("encryl.v2")):
				enc = 0
			}
			if enc > p.subEncoding {
				p.subEncoding = enc
				p.sub = append(p.sub[:0], token...)
			}
		}
	case bytes.EqualFold(name, []byte("origin")):
		if len(p.origin) > 0 {
			return &RejectError{Status: 400, Reason: "duplicate Origin"}
		}
		p.origin = append(p.origin[:0], value...)
	case bytes.EqualFold(name, []byte("content-length")):
		if !bytes.Equal(value, []byte("0")) {
			return &RejectError{Status: 400, Reason: "request body is not allowed"}
		}
	case bytes.EqualFold(name, []byte("transfer-encoding")):
		return &RejectError{Status: 400, Reason: "transfer encoding is not allowed"}
	}
	return nil
}

func (p *Parser) finish() error {
	if !p.request || !p.hasHost || !p.hasUp || !p.hasConn || !p.hasVer || !p.hasKey {
		return &RejectError{Status: 400, Reason: "missing required WebSocket headers"}
	}
	if p.badFormat {
		return &RejectError{Status: 400, Reason: "unsupported protocol format"}
	}
	if p.hasProto && p.subEncoding < 0 {
		return &RejectError{Status: 400, Reason: "Sec-WebSocket-Protocol must include encryl.v2 or encryl.bebop.v1"}
	}
	if p.formatBebop && p.hasProto && p.subEncoding == 0 {
		return &RejectError{Status: 400, Reason: "protocol format conflicts with encryl.v2 subprotocol"}
	}
	if p.formatBebop && p.hasProto && p.subEncoding != 1 {
		return &RejectError{Status: 400, Reason: "protocol format conflicts with the selected subprotocol"}
	}
	p.done = true
	return nil
}

func (p *Parser) acceptKey(key []byte) bool {
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
	base64.StdEncoding.Encode(p.accept[:], sum[:])
	return true
}

const webSocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Response101 writes the upgrade response into dst and returns its length. dst
// must have room for at least 256 bytes. subprotocol is echoed when it is not
// empty; a Bebop connection may negotiate with a query parameter only.
func Response101(dst []byte, accept [28]byte, subprotocol []byte) int {
	const prefix = "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: "
	n := copy(dst, prefix)
	n += copy(dst[n:], accept[:])
	if len(subprotocol) > 0 {
		n += copy(dst[n:], "\r\nSec-WebSocket-Protocol: ")
		n += copy(dst[n:], subprotocol)
	}
	n += copy(dst[n:], "\r\n\r\n")
	return n
}

// RejectResponse builds a small 4xx response. Handshake rejections are not on
// the steady-state path, so this may allocate.
func RejectResponse(status int, reason string) []byte {
	body := reason + "\n"
	out := make([]byte, 0, len(body)+96)
	out = append(out, "HTTP/1.1 "...)
	out = strconv.AppendInt(out, int64(status), 10)
	out = append(out, ' ')
	out = append(out, reason...)
	out = append(out, "\r\nConnection: close\r\nContent-Length: "...)
	out = strconv.AppendInt(out, int64(len(body)), 10)
	out = append(out, "\r\n\r\n"...)
	out = append(out, body...)
	return out
}

func trim(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

func splitTokens(value []byte) [][]byte {
	var out [][]byte
	for len(value) > 0 {
		i := bytes.IndexByte(value, ',')
		var token []byte
		if i < 0 {
			token, value = value, nil
		} else {
			token, value = value[:i], value[i+1:]
		}
		token = trim(token)
		if len(token) > 0 {
			out = append(out, token)
		}
	}
	return out
}

func containsToken(value, want []byte) bool {
	for len(value) > 0 {
		i := bytes.IndexByte(value, ',')
		var token []byte
		if i < 0 {
			token = value
			value = nil
		} else {
			token = value[:i]
			value = value[i+1:]
		}
		if bytes.EqualFold(trim(token), want) {
			return true
		}
	}
	return false
}

func validToken(b []byte) bool {
	for _, c := range b {
		if c <= 0x20 || c >= 0x7f {
			return false
		}
		switch c {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=', '{', '}':
			return false
		}
	}
	return true
}

func validValue(b []byte) bool {
	for _, c := range b {
		if c == 0 || c == '\r' || c == '\n' {
			return false
		}
		if c < 0x20 && c != '\t' {
			return false
		}
	}
	return true
}
