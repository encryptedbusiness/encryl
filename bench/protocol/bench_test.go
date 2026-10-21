package protocol_bench

import (
	"testing"

	"github.com/encryptedbusiness/encryl/protocol"
)

func BenchmarkDecodePublish(b *testing.B) {
	in := protocol.Command{ID: 1, Kind: protocol.CommandPublish, Publish: protocol.Publish{
		Room: []byte("chat"), Event: protocol.EventRef{ID: 1024}, Payload: make([]byte, 64),
	}}
	frame := protocol.AppendFramedCommand(nil, &in)
	var dst []protocol.Command
	dst, _ = protocol.DecodeCommands(frame, dst)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst, _ = protocol.DecodeCommands(frame, dst[:0])
	}
}

func BenchmarkDecodeBatch16(b *testing.B) {
	in := protocol.Command{ID: 1, Kind: protocol.CommandPublish, Publish: protocol.Publish{
		Room: []byte("chat"), Event: protocol.EventRef{ID: 1024}, Payload: make([]byte, 64),
	}}
	var frame []byte
	for i := 0; i < 16; i++ {
		frame = protocol.AppendFramedCommand(frame, &in)
	}
	var dst []protocol.Command
	dst, _ = protocol.DecodeCommands(frame, dst)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst, _ = protocol.DecodeCommands(frame, dst[:0])
	}
}

func BenchmarkEncodeEventPush(b *testing.B) {
	p := protocol.Push{Kind: protocol.PushEvent, Event: protocol.EventPush{
		Room: []byte("chat"), EventID: 1024, Payload: make([]byte, 64),
	}}
	buf := make([]byte, 0, protocol.FramedPushSize(&p))
	buf = protocol.AppendFramedPush(buf, &p)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = protocol.AppendFramedPush(buf[:0], &p)
	}
}

func BenchmarkEncodeMessagePush(b *testing.B) {
	p := protocol.Push{Kind: protocol.PushMessage, Message: protocol.Message{EventID: 1024, Payload: make([]byte, 64)}}
	buf := make([]byte, 0, protocol.FramedPushSize(&p))
	buf = protocol.AppendFramedPush(buf, &p)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = protocol.AppendFramedPush(buf[:0], &p)
	}
}
