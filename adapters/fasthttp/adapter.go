//go:build linux

// Package fasthttp adapts encryl to a fasthttp request handler.
package fasthttpadapter

import (
	"net"
	"time"

	encryl "github.com/encryptedbusiness/encryl"
	"github.com/valyala/fasthttp"
)

const maxBufferedHandoff = 64 << 10

// Handler returns a fasthttp handler that hijacks a WebSocket upgrade and
// gives the descriptor to srv.
func Handler(srv *encryl.Server) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		_ = Upgrade(ctx, srv)
	}
}

// Upgrade validates the parsed fasthttp request and schedules the hijack.
// Validation errors are written to ctx and return nil; the response is
// suppressed only after validation succeeds.
func Upgrade(ctx *fasthttp.RequestCtx, srv *encryl.Server) error {
	if srv == nil {
		ctx.Error("encryl is not started", fasthttp.StatusServiceUnavailable)
		return nil
	}
	if ctx.IsTLS() {
		ctx.Error("TLS must terminate upstream", fasthttp.StatusBadRequest)
		return nil
	}

	h := encryl.UpgradeHeaders{
		Method: ctx.Method(),
		Proto:  ctx.Request.Header.Protocol(),
		Target: ctx.RequestURI(),
		Host:   ctx.Host(),

		HostCount:          count(ctx.Request.Header.PeekAll("Host")),
		Origin:             ctx.Request.Header.Peek("Origin"),
		OriginCount:        len(ctx.Request.Header.PeekAll("Origin")),
		Key:                ctx.Request.Header.Peek("Sec-WebSocket-Key"),
		KeyCount:           len(ctx.Request.Header.PeekAll("Sec-WebSocket-Key")),
		Version:            ctx.Request.Header.Peek("Sec-WebSocket-Version"),
		VersionCount:       len(ctx.Request.Header.PeekAll("Sec-WebSocket-Version")),
		Protocol:           ctx.Request.Header.Peek("Sec-WebSocket-Protocol"),
		ProtocolCount:      len(ctx.Request.Header.PeekAll("Sec-WebSocket-Protocol")),
		ContentLength:      ctx.Request.Header.Peek("Content-Length"),
		ContentLengthCount: len(ctx.Request.Header.PeekAll("Content-Length")),
		TransferEnc:        ctx.Request.Header.Peek("Transfer-Encoding"),
		TransferEncCount:   len(ctx.Request.Header.PeekAll("Transfer-Encoding")),
		HasUpgrade:         hasToken(ctx.Request.Header.Peek("Upgrade"), "websocket"),
		HasConnectionUp:    hasToken(ctx.Request.Header.Peek("Connection"), "upgrade"),
		HasBody: len(ctx.PostBody()) > 0 ||
			ctx.Request.Header.ContentLength() > 0 ||
			len(ctx.Request.Header.Peek("Transfer-Encoding")) > 0,
	}
	if h.HostCount == 0 && len(h.Host) > 0 {
		h.HostCount = 1
	}

	spec, err := srv.PrepareUpgrade(h)
	if err != nil {
		if ue, ok := err.(*encryl.UpgradeError); ok {
			ctx.Error(ue.Reason, ue.Status)
		} else {
			ctx.Error(err.Error(), fasthttp.StatusBadRequest)
		}
		return nil
	}

	ctx.HijackSetNoResponse(true)
	ctx.Hijack(func(conn net.Conn) {
		base := conn
		if u, ok := conn.(interface{ UnsafeConn() net.Conn }); ok {
			base = u.UnsafeConn()
		}

		buffered := readBuffered(conn)
		if err := srv.Upgrade(base, spec, buffered); err != nil {
			_ = conn.Close()
			return
		}
		_ = conn.Close()
	})
	return nil
}

func count(v [][]byte) int { return len(v) }

func readBuffered(conn net.Conn) []byte {
	_ = conn.SetReadDeadline(time.Now())
	var (
		out []byte
		buf [4096]byte
	)
	for len(out) < maxBufferedHandoff {
		n, err := conn.Read(buf[:])
		if n > 0 {
			if len(out)+n > maxBufferedHandoff {
				n = maxBufferedHandoff - len(out)
			}
			out = append(out, buf[:n]...)
		}
		if err != nil || n == 0 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func hasToken(value []byte, want string) bool {
	for len(value) > 0 {
		i := 0
		for i < len(value) && value[i] != ',' {
			i++
		}
		token := trim(value[:i])
		if equalFold(token, want) {
			return true
		}
		if i == len(value) {
			break
		}
		value = value[i+1:]
	}
	return false
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

func equalFold(a []byte, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
