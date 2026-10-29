//go:build linux

package app

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/event"
	"github.com/gorilla/websocket"
)

func TestRealtimeFanoutToManyClients(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.SetHandler(&roomLifecycle{}); err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		return c.To("room").EmitID(1024, m.Payload)
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	const clients = 8
	conns := make([]*websocket.Conn, 0, clients)
	for i := 0; i < clients; i++ {
		c := dialWS(t, url)
		t.Cleanup(func() { _ = c.Close() })
		conns = append(conns, c)
	}

	deadline := time.Now().Add(3 * time.Second)
	for srv.Stats().Memberships < clients && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := srv.Stats().Memberships; got < clients {
		t.Fatalf("memberships = %d, want %d", got, clients)
	}

	writeEvent(t, conns[0], 1024, 0, []byte("fanout"))

	for i, c := range conns[1:] {
		e := readEnvelope(t, c)
		if e.Kind != event.KindEvent || e.Event != 1024 || string(e.Payload) != "fanout" {
			t.Fatalf("client %d event = %+v", i+1, e)
		}
	}
}

func TestRealtimeConcurrentEcho(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		return c.EmitID(1024, m.Payload)
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	const (
		clients   = 16
		perClient = 25
	)
	var wg sync.WaitGroup
	errCh := make(chan error, clients)
	for id := 0; id < clients; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			d := websocket.Dialer{
				HandshakeTimeout: 3 * time.Second,
				Subprotocols:     []string{"encryl.v2"},
			}
			c, _, err := d.Dial(url, nil)
			if err != nil {
				errCh <- fmt.Errorf("client %d dial: %w", id, err)
				return
			}
			defer c.Close()

			for msg := 0; msg < perClient; msg++ {
				payload := []byte(fmt.Sprintf("c%d-m%d", id, msg))
				frame := event.EncodeMessage(event.Envelope{
					Kind:  event.KindEvent,
					Event: 1024,
				}, payload)
				if err := c.WriteMessage(websocket.BinaryMessage, frame); err != nil {
					errCh <- fmt.Errorf("client %d write: %w", id, err)
					return
				}
				_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
				_, data, err := c.ReadMessage()
				if err != nil {
					errCh <- fmt.Errorf("client %d read: %w", id, err)
					return
				}
				e, err := event.Decode(data)
				if err != nil {
					errCh <- fmt.Errorf("client %d decode: %w", id, err)
					return
				}
				if string(e.Payload) != string(payload) {
					errCh <- fmt.Errorf("client %d echo = %q, want %q", id, e.Payload, payload)
					return
				}
			}
		}(id)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

type hookProbe struct {
	opened chan struct{}
	closed chan CloseInfo
}

func (p *hookProbe) OnOpen(*Conn) error {
	select {
	case p.opened <- struct{}{}:
	default:
	}
	return nil
}

func (p *hookProbe) OnMessage(*Conn, Message) error { return nil }

func (p *hookProbe) OnClose(_ *Conn, info CloseInfo) {
	select {
	case p.closed <- info:
	default:
	}
}

func TestRealtimeConnectDisconnectHooks(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	probe := &hookProbe{
		opened: make(chan struct{}, 4),
		closed: make(chan CloseInfo, 4),
	}
	if err := srv.SetHandler(probe); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	c := dialWS(t, url)
	select {
	case <-probe.opened:
	case <-time.After(2 * time.Second):
		t.Fatal("OnOpen was not called")
	}

	_ = c.Close()
	select {
	case info := <-probe.closed:
		if !info.Remote {
			t.Fatalf("OnClose did not report a remote close: %+v", info)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnClose was not called")
	}
}

func TestRealtimeReconnectChurn(t *testing.T) {
	srv, err := New(testServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.OnID(1024, func(c *Conn, m Message) error {
		return c.EmitID(1024, m.Payload)
	}); err != nil {
		t.Fatal(err)
	}
	url := startTestServer(t, srv)

	for i := 0; i < 25; i++ {
		c := dialWS(t, url)
		payload := []byte(fmt.Sprintf("churn-%d", i))
		writeEvent(t, c, 1024, 0, payload)
		e := readEnvelope(t, c)
		if string(e.Payload) != string(payload) {
			t.Fatalf("iteration %d echo = %q", i, e.Payload)
		}
		if err := c.Close(); err != nil {
			t.Fatalf("iteration %d close: %v", i, err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for srv.Stats().ConnectionsOpen != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := srv.Stats().ConnectionsOpen; got != 0 {
		t.Fatalf("connections still open after churn: %d", got)
	}
	if srv.Stats().ConnectionsAccepted < 25 {
		t.Fatalf("accepted = %d, want >= 25", srv.Stats().ConnectionsAccepted)
	}
}
