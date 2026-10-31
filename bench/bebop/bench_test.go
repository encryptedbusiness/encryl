// Package bebop benchmarks the Bebop event codec.
package bebop

import (
	"testing"

	"github.com/encryptedbusiness/encryl/protocol"
)

func TestWireSizes(t *testing.T) {
	cmds := []struct {
		name string
		c    protocol.Command
	}{
		{"publish-room-name", protocol.Command{ID: 1, Kind: protocol.CommandPublish, Publish: protocol.Publish{
			Room: []byte("chat"), Event: protocol.EventRef{ID: 1024}, Payload: make([]byte, 64),
		}}},
		{"publish-room-id", protocol.Command{ID: 1, Kind: protocol.CommandPublish, Publish: protocol.Publish{
			RoomID: 7, Event: protocol.EventRef{ID: 1024}, Payload: make([]byte, 64),
		}}},
		{"send", protocol.Command{ID: 1, Kind: protocol.CommandSend, Send: protocol.Send{
			Event: protocol.EventRef{ID: 1024}, Payload: make([]byte, 64),
		}}},
	}
	for _, tc := range cmds {
		t.Logf("command %-18s bebop=%3d", tc.name, protocol.FramedCommandSizeBebop(&tc.c))
	}

	pushes := []struct {
		name string
		p    protocol.Push
	}{
		{"event", protocol.Push{Kind: protocol.PushEvent, Event: protocol.EventPush{
			Room: []byte("chat"), EventID: 1024, Payload: make([]byte, 64),
		}}},
		{"event-room-id", protocol.Push{Kind: protocol.PushEvent, Event: protocol.EventPush{
			RoomID: 7, EventID: 1024, Payload: make([]byte, 64),
		}}},
		{"message", protocol.Push{Kind: protocol.PushMessage, Message: protocol.Message{
			EventID: 1024, Payload: make([]byte, 64),
		}}},
	}
	for _, tc := range pushes {
		t.Logf("push    %-18s bebop=%3d", tc.name, protocol.FramedPushSizeBebop(&tc.p))
	}
}

func BenchmarkDecodePublish(b *testing.B) {
	in := protocol.Command{ID: 1, Kind: protocol.CommandPublish, Publish: protocol.Publish{
		Room: []byte("chat"), Event: protocol.EventRef{ID: 1024}, Payload: make([]byte, 64),
	}}
	frame := protocol.AppendFramedCommandBebop(nil, &in)
	var dst []protocol.Command
	dst, _ = protocol.DecodeBebopCommands(frame, dst)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst, _ = protocol.DecodeBebopCommands(frame, dst[:0])
	}
}

func BenchmarkDecodeBatch16(b *testing.B) {
	in := protocol.Command{ID: 1, Kind: protocol.CommandPublish, Publish: protocol.Publish{
		Room: []byte("chat"), Event: protocol.EventRef{ID: 1024}, Payload: make([]byte, 64),
	}}
	var frame []byte
	for i := 0; i < 16; i++ {
		frame = protocol.AppendFramedCommandBebop(frame, &in)
	}
	var dst []protocol.Command
	dst, _ = protocol.DecodeBebopCommands(frame, dst)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst, _ = protocol.DecodeBebopCommands(frame, dst[:0])
	}
}

func BenchmarkEncodeEventPush(b *testing.B) {
	p := protocol.Push{Kind: protocol.PushEvent, Event: protocol.EventPush{
		Room: []byte("chat"), EventID: 1024, Payload: make([]byte, 64),
	}}
	buf := make([]byte, 0, protocol.FramedPushSizeBebop(&p))
	buf = protocol.AppendFramedPushBebop(buf, &p)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = protocol.AppendFramedPushBebop(buf[:0], &p)
	}
}

func BenchmarkEncodeMessagePush(b *testing.B) {
	p := protocol.Push{Kind: protocol.PushMessage, Message: protocol.Message{
		EventID: 1024, Payload: make([]byte, 64),
	}}
	buf := make([]byte, 0, protocol.FramedPushSizeBebop(&p))
	buf = protocol.AppendFramedPushBebop(buf, &p)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = protocol.AppendFramedPushBebop(buf[:0], &p)
	}
}

// BenchmarkFanout1000 models one encode per broadcast followed by a copy of
// the encoded frame to each recipient's write queue.
func BenchmarkFanout1000(b *testing.B) {
	p := protocol.Push{Kind: protocol.PushEvent, Event: protocol.EventPush{
		Room: []byte("chat"), EventID: 1024, Payload: make([]byte, 64),
	}}
	const recipients = 1000
	frame := make([]byte, 0, protocol.FramedPushSizeBebop(&p))
	frame = protocol.AppendFramedPushBebop(frame, &p)
	dst := make([]byte, len(frame)*recipients)
	b.SetBytes(int64(len(frame) * recipients))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		frame = protocol.AppendFramedPushBebop(frame[:0], &p)
		off := 0
		for j := 0; j < recipients; j++ {
			off += copy(dst[off:], frame)
		}
	}
}
