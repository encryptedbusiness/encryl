package event

import (
	"encoding/hex"
	"errors"
	"testing"
)

func TestEventVector(t *testing.T) {
	wire, err := hex.DecodeString("020200000000040001020304050607086869")
	if err != nil {
		t.Fatal(err)
	}
	e, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != KindEvent || e.Event != 1024 || e.RequestID != 0x0102030405060708 {
		t.Fatalf("decoded = %+v", e)
	}
	if string(e.Payload) != "hi" {
		t.Fatalf("payload = %q", e.Payload)
	}
}

func TestAuthVector(t *testing.T) {
	wire, err := hex.DecodeString("0201000000000000000000000000000101616263")
	if err != nil {
		t.Fatal(err)
	}
	e, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != KindAuth || e.Event != 0 || e.RequestID != 1 || string(e.Payload) != "\x01abc" {
		t.Fatalf("decoded = %+v", e)
	}
}

func TestAckVector(t *testing.T) {
	wire, err := hex.DecodeString("02030002000004000102030405060708")
	if err != nil {
		t.Fatal(err)
	}
	e, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != KindAck || e.Flags != FlagNoReply || e.Event != 1024 || e.RequestID != 0x0102030405060708 {
		t.Fatalf("decoded = %+v", e)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	src := Envelope{Kind: KindEvent, Flags: 1 << 0, Event: 2048, RequestID: 99, Payload: []byte("body")}
	wire := EncodeMessage(src, src.Payload)
	got, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != src.Kind || got.Flags != src.Flags || got.Event != src.Event || got.RequestID != src.RequestID || string(got.Payload) != "body" {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		wire []byte
		want error
	}{
		{"short", make([]byte, 15), ErrShort},
		{"version", func() []byte { b := make([]byte, 16); b[0] = 3; b[1] = 2; return b }(), ErrVersion},
		{"flags", func() []byte { b := make([]byte, 16); b[0] = 2; b[1] = 2; b[3] = 0x04; return b }(), ErrFlags},
		{"kind", func() []byte { b := make([]byte, 16); b[0] = 2; b[1] = 9; return b }(), ErrKind},
		{"event zero", func() []byte { b := make([]byte, 16); b[0] = 2; b[1] = 2; return b }(), ErrEvent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(tc.wire)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDecodeDoesNotAllocate(t *testing.T) {
	wire, err := hex.DecodeString("020200000000040001020304050607086869")
	if err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		_, _ = Decode(wire)
	})
	if allocs != 0 {
		t.Fatalf("Decode allocated %.2f times", allocs)
	}
}

func FuzzEnvelope(f *testing.F) {
	f.Add([]byte{2, 2, 0, 0, 0, 0, 4, 0, 1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		e, err := Decode(data)
		if err != nil {
			return
		}
		if len(e.Payload) != len(data)-HeaderSize {
			t.Fatalf("payload length = %d, want %d", len(e.Payload), len(data)-HeaderSize)
		}
	})
}
