//go:build linux

package app

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/event"
	"github.com/gorilla/websocket"
)

func TestRealtimeRejectsDisallowedOrigin(t *testing.T) {
	cfg := testServerConfig()
	cfg.AllowedOrigins = []string{"https://app.example"}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	d := websocket.Dialer{
		HandshakeTimeout: 2 * time.Second,
		Subprotocols:     []string{"encryl.v2"},
	}
	c, resp, err := d.Dial(url, http.Header{"Origin": []string{"https://evil.example"}})
	if c != nil {
		_ = c.Close()
	}
	if err == nil {
		t.Fatal("handshake from a disallowed origin succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestRealtimeAcceptsAllowedOrigin(t *testing.T) {
	cfg := testServerConfig()
	cfg.AllowedOrigins = []string{"https://app.example"}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	d := websocket.Dialer{
		HandshakeTimeout: 2 * time.Second,
		Subprotocols:     []string{"encryl.v2"},
	}
	c, _, err := d.Dial(url, http.Header{"Origin": []string{"https://app.example"}})
	if err != nil {
		t.Fatalf("allowed origin rejected: %v", err)
	}
	_ = c.Close()
}

func TestRealtimeRejectsOversizedFrame(t *testing.T) {
	cfg := testServerConfig()
	cfg.MaxFrameSize = 256
	cfg.MaxMessageSize = 1024
	cfg.ReadBufferSize = 256
	cfg.WriteQueueBytes = 1 << 20
	cfg.TxBytes = 1 << 20
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	c := dialWS(t, url)
	defer c.Close()

	frame := event.EncodeMessage(event.Envelope{
		Kind:  event.KindEvent,
		Event: 1,
	}, make([]byte, 2048))
	if err := c.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = c.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != 1009 {
		t.Fatalf("close = %v, want 1009", err)
	}
}

func TestRealtimeRejectsMalformedUpgrade(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	// A plain HTTP GET is not an upgrade request.
	resp, err := http.Get(strings.Replace(url, "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		t.Fatal("plain HTTP GET was upgraded")
	}
}
