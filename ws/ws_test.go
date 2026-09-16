package ws

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

var testMask = [4]byte{0x37, 0xfa, 0x21, 0x3d}

func frame(op Opcode, fin bool, payload []byte) []byte {
	b0 := byte(op)
	if fin {
		b0 |= 0x80
	}
	var out []byte
	n := len(payload)
	switch {
	case n < 126:
		out = make([]byte, 2, 2+n+4)
		out[0] = b0
		out[1] = 0x80 | byte(n)
	case n <= 65535:
		out = make([]byte, 4, 4+n+4)
		out[0] = b0
		out[1] = 0x80 | 126
		binary.BigEndian.PutUint16(out[2:4], uint16(n))
	default:
		out = make([]byte, 10, 10+n+4)
		out[0] = b0
		out[1] = 0x80 | 127
		binary.BigEndian.PutUint64(out[2:10], uint64(n))
	}
	out = append(out, testMask[:]...)
	masked := make([]byte, n)
	copy(masked, payload)
	Mask(masked, testMask, 0)
	return append(out, masked...)
}

func unmaskedFrame(op Opcode, fin bool, payload []byte) []byte {
	b0 := byte(op)
	if fin {
		b0 |= 0x80
	}
	out := []byte{b0, byte(len(payload))}
	return append(out, payload...)
}

func collect() ([]Event, Handler, func() []Event) {
	var events []Event
	h := func(e Event) error {
		cp := e
		cp.Payload = append([]byte(nil), e.Payload...)
		events = append(events, cp)
		return nil
	}
	return events, h, func() []Event { return events }
}

func feedChunks(t *testing.T, p *Parser, data []byte, step int, h Handler) {
	t.Helper()
	for len(data) > 0 {
		n := step
		if n > len(data) {
			n = len(data)
		}
		if err := p.Feed(data[:n], h); err != nil {
			t.Fatalf("Feed: %v", err)
		}
		data = data[n:]
	}
}

func TestSingleBinaryFrame(t *testing.T) {
	p := NewParser(DefaultLimits())
	_, h, get := collect()
	msg := []byte("hello")
	if err := p.Feed(frame(Binary, true, msg), h); err != nil {
		t.Fatal(err)
	}
	events := get()
	if len(events) != 1 || events[0].Kind != EventMessage || string(events[0].Payload) != "hello" {
		t.Fatalf("events = %+v", events)
	}
}

func TestSingleFrameEverySplit(t *testing.T) {
	msg := bytes.Repeat([]byte("abcdef"), 100)
	wire := frame(Binary, true, msg)
	for step := 1; step <= len(wire); step++ {
		p := NewParser(DefaultLimits())
		var got []byte
		h := func(e Event) error { got = append(got, e.Payload...); return nil }
		w := append([]byte(nil), wire...)
		feedChunks(t, p, w, step, h)
		if !bytes.Equal(got, msg) {
			t.Fatalf("step %d: payload mismatch (got %d bytes)", step, len(got))
		}
	}
}

func TestMultipleFramesOneBuffer(t *testing.T) {
	p := NewParser(DefaultLimits())
	_, h, get := collect()
	wire := append(frame(Binary, true, []byte("one")), frame(Binary, true, []byte("two"))...)
	if err := p.Feed(wire, h); err != nil {
		t.Fatal(err)
	}
	events := get()
	if len(events) != 2 || string(events[0].Payload) != "one" || string(events[1].Payload) != "two" {
		t.Fatalf("events = %+v", events)
	}
}

func TestFragmentedMessage(t *testing.T) {
	p := NewParser(DefaultLimits())
	_, h, get := collect()
	wire := append(frame(Binary, false, []byte("hel")), frame(Continuation, false, []byte("lo "))...)
	wire = append(wire, frame(Continuation, true, []byte("world"))...)
	if err := p.Feed(wire, h); err != nil {
		t.Fatal(err)
	}
	events := get()
	if len(events) != 1 || string(events[0].Payload) != "hello world" {
		t.Fatalf("events = %+v", events)
	}
}

func TestPingInterleavedMidFragment(t *testing.T) {
	p := NewParser(DefaultLimits())
	_, h, get := collect()
	wire := append(frame(Binary, false, []byte("a")), frame(Ping, true, []byte("p"))...)
	wire = append(wire, frame(Continuation, true, []byte("b"))...)
	if err := p.Feed(wire, h); err != nil {
		t.Fatal(err)
	}
	events := get()
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Kind != EventPing || string(events[0].Payload) != "p" {
		t.Fatalf("ping = %+v", events[0])
	}
	if events[1].Kind != EventMessage || string(events[1].Payload) != "ab" {
		t.Fatalf("message = %+v", events[1])
	}
}

func TestZeroLengthFrames(t *testing.T) {
	p := NewParser(DefaultLimits())
	_, h, get := collect()
	if err := p.Feed(frame(Binary, true, nil), h); err != nil {
		t.Fatal(err)
	}
	if err := p.Feed(frame(Binary, false, nil), h); err != nil {
		t.Fatal(err)
	}
	if err := p.Feed(frame(Continuation, true, nil), h); err != nil {
		t.Fatal(err)
	}
	events := get()
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Kind != EventMessage || len(events[0].Payload) != 0 {
		t.Fatalf("first = %+v", events[0])
	}
	if events[1].Kind != EventMessage || len(events[1].Payload) != 0 {
		t.Fatalf("second = %+v", events[1])
	}
}

func TestCloseFrames(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		code    uint16
		reason  string
		errCode uint16
	}{
		{"empty", nil, 1005, "", 0},
		{"normal", []byte{0x03, 0xe8, 'b', 'y', 'e'}, 1000, "bye", 0},
		{"one byte", []byte{0x03}, 0, "", 1002},
		{"invalid code", []byte{0x03, 0xed}, 0, "", 1002},
		{"invalid utf8", []byte{0x03, 0xe8, 0xff}, 0, "", 1007},
		{"app code", []byte{0x0f, 0xa0}, 4000, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser(DefaultLimits())
			_, h, get := collect()
			err := p.Feed(frame(Close, true, tc.payload), h)
			if tc.errCode != 0 {
				var pe *ProtocolError
				if !errors.As(err, &pe) || pe.Code != tc.errCode {
					t.Fatalf("err = %v, want code %d", err, tc.errCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			events := get()
			if len(events) != 1 || events[0].Kind != EventClose || events[0].CloseCode != tc.code || events[0].CloseReason != tc.reason {
				t.Fatalf("events = %+v", events)
			}
		})
	}
}

func TestProtocolErrors(t *testing.T) {
	tests := []struct {
		name string
		wire []byte
		code uint16
	}{
		{"unmasked", unmaskedFrame(Binary, true, []byte("x")), 1002},
		{"text", frame(Text, true, []byte("x")), 1003},
		{"rsv", func() []byte { f := frame(Binary, true, []byte("x")); f[0] |= 0x40; return f }(), 1002},
		{"continuation without message", frame(Continuation, true, []byte("x")), 1002},
		{"unknown opcode", frame(Opcode(0x3), true, []byte("x")), 1002},
		{"fragmented control", frame(Ping, false, []byte("x")), 1002},
		{"oversized control", frame(Ping, true, bytes.Repeat([]byte("x"), 126)), 1002},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser(DefaultLimits())
			err := p.Feed(tc.wire, func(Event) error { return nil })
			var pe *ProtocolError
			if !errors.As(err, &pe) || pe.Code != tc.code {
				t.Fatalf("err = %v, want code %d", err, tc.code)
			}
		})
	}
}

func TestNewDataFrameDuringFragmentation(t *testing.T) {
	p := NewParser(DefaultLimits())
	wire := append(frame(Binary, false, []byte("a")), frame(Binary, true, []byte("b"))...)
	err := p.Feed(wire, func(Event) error { return nil })
	var pe *ProtocolError
	if !errors.As(err, &pe) || pe.Code != 1002 {
		t.Fatalf("err = %v", err)
	}
}

func TestLengthLimits(t *testing.T) {
	limits := Limits{MaxFrameSize: 130, MaxMessageSize: 128, MaxFragments: 1}

	t.Run("frame", func(t *testing.T) {
		p := NewParser(limits)
		err := p.Feed(frame(Binary, true, bytes.Repeat([]byte("x"), 131)), func(Event) error { return nil })
		var pe *ProtocolError
		if !errors.As(err, &pe) || pe.Code != 1009 {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("message", func(t *testing.T) {
		limits := Limits{MaxFrameSize: 200, MaxMessageSize: 128, MaxFragments: 10}
		p := NewParser(limits)
		err := p.Feed(frame(Binary, true, bytes.Repeat([]byte("x"), 129)), func(Event) error { return nil })
		var pe *ProtocolError
		if !errors.As(err, &pe) || pe.Code != 1009 {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("fragments", func(t *testing.T) {
		p := NewParser(limits)
		wire := append(frame(Binary, false, []byte("a")), frame(Continuation, false, []byte("b"))...)
		wire = append(wire, frame(Continuation, true, []byte("c"))...)
		err := p.Feed(wire, func(Event) error { return nil })
		var pe *ProtocolError
		if !errors.As(err, &pe) || pe.Code != 1009 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestNonMinimalLengths(t *testing.T) {
	t.Run("16-bit", func(t *testing.T) {
		wire := []byte{0x82, 0xfe, 0x00, 0x05, 0x37, 0xfa, 0x21, 0x3d, 'h', 'e', 'l', 'l', 'o'}
		p := NewParser(DefaultLimits())
		err := p.Feed(wire, func(Event) error { return nil })
		var pe *ProtocolError
		if !errors.As(err, &pe) || pe.Code != 1002 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("64-bit high bit", func(t *testing.T) {
		wire := []byte{0x82, 0xff, 0x80, 0, 0, 0, 0, 0, 0, 0, 0x37, 0xfa, 0x21, 0x3d}
		p := NewParser(DefaultLimits())
		err := p.Feed(wire, func(Event) error { return nil })
		var pe *ProtocolError
		if !errors.As(err, &pe) || pe.Code != 1002 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestPayloadSplitAcrossManyChunks(t *testing.T) {
	msg := bytes.Repeat([]byte("0123456789"), 50)
	wire := frame(Binary, true, msg)
	for _, step := range []int{1, 2, 3, 5, 7, 11, 64} {
		p := NewParser(DefaultLimits())
		var got []byte
		h := func(e Event) error { got = append(got, e.Payload...); return nil }
		w := append([]byte(nil), wire...)
		feedChunks(t, p, w, step, h)
		if !bytes.Equal(got, msg) {
			t.Fatalf("step %d: mismatch", step)
		}
	}
}

func TestResetReleasesSpill(t *testing.T) {
	p := NewParser(DefaultLimits())
	_ = p.Feed(frame(Binary, false, []byte("abc")), func(Event) error { return nil })
	if p.extra == nil || p.extra.spill == nil {
		t.Fatal("expected a spill buffer")
	}
	p.Reset()
	if p.extra != nil {
		t.Fatal("Reset did not release the spill buffer")
	}
}

func TestSingleFrameDoesNotAllocate(t *testing.T) {
	p := NewParser(DefaultLimits())
	wire := frame(Binary, true, []byte("hello"))
	h := func(Event) error { return nil }

	buf := make([]byte, len(wire))
	p.Feed(wire, h)
	allocs := testing.AllocsPerRun(1000, func() {
		copy(buf, wire)
		p.Reset()
		_ = p.Feed(buf, h)
	})
	if allocs != 0 {
		t.Fatalf("Feed allocated %.2f times, want 0", allocs)
	}
}

func FuzzFrame(f *testing.F) {
	f.Add(frame(Binary, true, []byte("hello")))
	f.Add(frame(Binary, false, []byte("a")))
	f.Add(frame(Continuation, true, []byte("b")))
	f.Add(frame(Close, true, []byte{0x03, 0xe8}))
	f.Add(unmaskedFrame(Binary, true, []byte("x")))

	f.Fuzz(func(t *testing.T, data []byte) {
		p := NewParser(DefaultLimits())
		_ = p.Feed(data, func(e Event) error {
			if len(e.Payload) > 1<<20 {
				t.Fatalf("payload too large: %d", len(e.Payload))
			}
			return nil
		})
	})
}
