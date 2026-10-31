package protocol

import "testing"

// FuzzDecodeBebopCommands checks that a hostile frame cannot panic the Bebop
// decoder.
func FuzzDecodeBebopCommands(f *testing.F) {
	seeds := [][]byte{
		{},
		{0x00},
		{0x01, 0x00},
		{0x05, 0x01, 0x00, 0x00, 0x00, 0x04, 0x00},
		{0x05, 0x01, 0x00, 0x00, 0x00, 0x04, 0x10, 0x04, 0x00, 0x00, 0x00, 0x00},
		{0xff, 0xff, 0xff, 0xff, 0x0f},
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var cmds []Command
		_, _ = DecodeBebopCommands(data, cmds)
		var replies []Reply
		_, _ = DecodeBebopReplies(data, replies)
	})
}
