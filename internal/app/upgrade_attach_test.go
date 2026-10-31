//go:build linux

package app

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"
	"testing"

	upgrade "github.com/encryptedbusiness/encryl/httpx"
)

const testWSKey = "dGhlIHNhbXBsZSBub25jZQ=="

func rawUpgrade(extra string) string {
	return "GET /ws HTTP/1.1\r\n" +
		"Host: example.com\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + testWSKey + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		extra + "\r\n"
}

func headersForRaw(t *testing.T, raw string) (UpgradeHeaders, *http.Request) {
	t.Helper()
	r, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	return upgradeHeadersFromNetHTTP(r), r
}

func parseRaw(t *testing.T, raw string) (*upgrade.Parser, error) {
	t.Helper()
	p := upgrade.NewParser(upgrade.DefaultLimits())
	res, _, err := p.Feed([]byte(raw))
	if err != nil {
		return p, err
	}
	if res != upgrade.Complete {
		t.Fatalf("parser result = %v, want complete", res)
	}
	return p, nil
}

func TestPrepareUpgradeParity(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"native", rawUpgrade("Sec-WebSocket-Protocol: encryl.v2\r\n")},
		{"bebop", rawUpgrade("Sec-WebSocket-Protocol: encryl.bebop.v1\r\n")},
		{"default-bebop", rawUpgrade("")},
		{"query-bebop", "GET /ws?format=bebop HTTP/1.1\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + testWSKey + "\r\nSec-WebSocket-Version: 13\r\n\r\n"},
		{"query-json", "GET /ws?format=json HTTP/1.1\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + testWSKey + "\r\nSec-WebSocket-Version: 13\r\n\r\n"},
		{"unknown-protocol", rawUpgrade("Sec-WebSocket-Protocol: other\r\n")},
		{"bad-version", "GET /ws HTTP/1.1\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + testWSKey + "\r\nSec-WebSocket-Version: 12\r\n\r\n"},
		{"bad-key", "GET /ws HTTP/1.1\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: nope\r\nSec-WebSocket-Version: 13\r\n\r\n"},
		{"body", "GET /ws HTTP/1.1\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + testWSKey + "\r\nSec-WebSocket-Version: 13\r\nContent-Length: 1\r\n\r\n"},
		{"post", "POST /ws HTTP/1.1\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + testWSKey + "\r\nSec-WebSocket-Version: 13\r\n\r\n"},
		{"http10", "GET /ws HTTP/1.0\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + testWSKey + "\r\nSec-WebSocket-Version: 13\r\n\r\n"},
	}

	srv := &Server{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := headersForRaw(t, tc.raw)
			spec, err := srv.PrepareUpgrade(h)
			p, perr := parseRaw(t, tc.raw)

			if err != nil {
				var ue *UpgradeError
				if !asUpgradeError(err, &ue) {
					t.Fatalf("PrepareUpgrade error type %T", err)
				}
				var rej *upgrade.RejectError
				if perr == nil || !asRejectError(perr, &rej) {
					t.Fatalf("parser accepted rejected request: prepare=%v parser=%v", err, perr)
				}
				if ue.Status != rej.Status {
					t.Fatalf("status = %d, parser = %d", ue.Status, rej.Status)
				}
				return
			}
			if perr != nil {
				t.Fatalf("parser rejected accepted request: %v", perr)
			}
			req := p.Request()
			if spec.Accept != req.Accept {
				t.Fatalf("accept = %q, want %q", spec.Accept, req.Accept)
			}
			if spec.Encoding != req.Encoding {
				t.Fatalf("encoding = %d, want %d", spec.Encoding, req.Encoding)
			}
			if got, want := spec.Subprotocol[:spec.SubprotocolLen], req.Subprotocol; !bytes.Equal(got, want) {
				t.Fatalf("subprotocol = %q, want %q", got, want)
			}
		})
	}
}

func TestPrepareUpgradeOrigin(t *testing.T) {
	h, _ := headersForRaw(t, rawUpgrade("Origin: https://evil.example\r\nSec-WebSocket-Protocol: encryl.v2\r\n"))

	srv := &Server{}
	if _, err := srv.PrepareUpgrade(h); err == nil {
		t.Fatal("mismatched origin accepted")
	}
	srv.cfg.AllowedOrigins = []string{"https://evil.example"}
	if _, err := srv.PrepareUpgrade(h); err != nil {
		t.Fatalf("allowed origin rejected: %v", err)
	}
	srv.cfg.AllowedOrigins = []string{"*"}
	if _, err := srv.PrepareUpgrade(h); err != nil {
		t.Fatalf("wildcard origin rejected: %v", err)
	}
}

func TestPrepareUpgradeZeroAlloc(t *testing.T) {
	h, _ := headersForRaw(t, rawUpgrade("Sec-WebSocket-Protocol: encryl.v2\r\n"))
	srv := &Server{}
	allocs := testing.AllocsPerRun(1000, func() {
		if _, err := srv.PrepareUpgrade(h); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("PrepareUpgrade allocs = %v, want 0", allocs)
	}
}

func asUpgradeError(err error, out **UpgradeError) bool {
	ue, ok := err.(*UpgradeError)
	if ok {
		*out = ue
	}
	return ok
}

func asRejectError(err error, out **upgrade.RejectError) bool {
	rej, ok := err.(*upgrade.RejectError)
	if ok {
		*out = rej
	}
	return ok
}
