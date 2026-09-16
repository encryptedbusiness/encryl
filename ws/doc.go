// Package ws implements the server-side RFC 6455 frame parser and builder.
//
// The parser is incremental: one WebSocket message may span several reads. It
// owns masking checks, fragmentation limits, and control-frame handling.
//
//	p := ws.NewParser(ws.DefaultLimits())
//	err := p.Feed(buf, func(e ws.Event) error { return handle(e) })
package ws
