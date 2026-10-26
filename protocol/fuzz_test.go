package protocol

import "testing"

// FuzzDecodeCommands checks that a hostile frame cannot panic the decoder.
func FuzzDecodeCommands(f *testing.F) {
	seeds := [][]byte{
		{},
		{0x00},
		{0x01, 0x00},
		{0x02, 0x08, 0x01},
		{0x05, 0x28, 0x02, 0x12, 0x00},
		{0xff, 0xff, 0xff, 0xff, 0x0f},
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var cmds []Command
		_, _ = DecodeCommands(data, cmds)
		var replies []Reply
		_, _ = DecodeReplies(data, replies)
	})
}
