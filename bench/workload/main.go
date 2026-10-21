//go:build linux

// Command workload runs practical message-delivery workloads against one
// encryl server process.
//
// Roles:
//
//	-role server   run the server with the handlers for the chosen workload
//	-role client   open -conns connections and drive the workload
//
// Workloads:
//
//	unicast  1:1 delivery. Each connection sends to a random peer.
//	group    group chat. Every room has -room-size members and one publisher.
//	ack      request/response. Each request waits for the ACK.
//	churn    connect, exchange one message, close. Checks membership cleanup.
//
// Client latency is measured end to end on one process clock.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl"
	"github.com/gorilla/websocket"
)

const (
	evWelcome = 2000
	evUnicast = 2001
	evDeliver = 2002
	evGroup   = 2003
	evGroupIn = 2004
	evAck     = 2005
)

func main() {
	role := flag.String("role", "server", "server or client")
	addr := flag.String("addr", "127.0.0.1:18090", "listen or dial address")
	workload := flag.String("workload", "group", "unicast, group, ack, or churn")
	conns := flag.Int("conns", 10000, "client connections, or server registry size")
	rate := flag.Int("rate", 1000, "aggregate messages per second")
	duration := flag.Duration("duration", 60*time.Second, "client run time")
	roomSize := flag.Int("room-size", 100, "members per group room")
	loops := flag.Int("loops", 0, "server event loops; 0 uses NumCPU")
	heapProfile := flag.String("heap-profile", "", "write a heap profile on SIGUSR2")
	srcIPs := flag.Int("src-ips", 20, "spread clients over 127.0.0.N addresses")
	msgSize := flag.Int("msg-size", 32, "payload bytes after the timestamp")
	workers := flag.Int("workers", 10, "churn workers")
	churnGap := flag.Duration("churn-gap", 5*time.Millisecond, "pause between churn cycles")
	flag.Parse()

	if *role == "server" {
		runServer(*addr, *workload, *conns, *roomSize, *loops, *heapProfile)
		return
	}
	runClient(*addr, *workload, *conns, *rate, *duration, *roomSize, *srcIPs, *msgSize, *workers, *churnGap)
}

// ---------------------------------------------------------------- server

type serverState struct {
	ids    []atomic.Uint64
	next   atomic.Uint32
	groups int
}

func runServer(addr, workload string, conns, roomSize, loops int, heapProfile string) {
	cfg := encryl.DefaultConfig()
	cfg.Addr = addr
	cfg.AllowedOrigins = []string{"*"}
	cfg.MaxConnections = conns + 1024
	cfg.Loops = loops
	cfg.MaxRoomsPerConn = 8
	cfg.PingInterval = 0
	cfg.IdleTimeout = 24 * time.Hour

	srv, err := encryl.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	st := &serverState{ids: make([]atomic.Uint64, conns)}
	if roomSize <= 0 {
		roomSize = 1
	}
	st.groups = (conns + roomSize - 1) / roomSize
	if st.groups < 1 {
		st.groups = 1
	}

	if err := srv.SetHandler(&lifecycle{st: st, workload: workload, groups: st.groups}); err != nil {
		log.Fatal(err)
	}
	if err := srv.OnID(evUnicast, func(c *encryl.Conn, m encryl.Message) error {
		p := m.Payload
		if len(p) < 12 {
			return nil
		}
		target := binary.BigEndian.Uint32(p[0:4])
		if int(target) >= len(st.ids) {
			return nil
		}
		packed := st.ids[target].Load()
		if packed == 0 {
			return nil
		}
		id := encryl.ConnID{Slot: uint32(packed), Gen: uint32(packed >> 32)}
		return srv.ToConns(id).EmitID(evDeliver, p[4:])
	}); err != nil {
		log.Fatal(err)
	}
	if err := srv.OnID(evGroup, func(c *encryl.Conn, m encryl.Message) error {
		p := m.Payload
		if len(p) < 12 {
			return nil
		}
		idx := binary.BigEndian.Uint32(p[0:4])
		room := fmt.Sprintf("g%d", idx%uint32(st.groups))
		return c.To(room).EmitID(evGroupIn, p[4:])
	}); err != nil {
		log.Fatal(err)
	}
	if err := srv.OnID(evAck, func(c *encryl.Conn, m encryl.Message) error {
		return c.Reply(m, m.Payload)
	}); err != nil {
		log.Fatal(err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	go func() {
		for range sig {
			log.Printf("stats: %+v room=%d", srv.Stats(), srv.RoomSize("g0"))
		}
	}()
	if heapProfile != "" {
		prof := make(chan os.Signal, 1)
		signal.Notify(prof, syscall.SIGUSR2)
		go func() {
			for range prof {
				f, err := os.Create(heapProfile)
				if err != nil {
					continue
				}
				_ = pprof.WriteHeapProfile(f)
				_ = f.Close()
			}
		}()
	}

	log.Printf("workload server: %s, addr=%s, conns=%d, groups=%d", workload, addr, conns, st.groups)
	log.Fatal(srv.ListenAndServe(context.Background()))
}

type lifecycle struct {
	st       *serverState
	workload string
	groups   int
}

func (l *lifecycle) OnOpen(c *encryl.Conn) error {
	idx := l.st.next.Add(1) - 1
	if int(idx) >= len(l.st.ids) {
		idx = idx % uint32(len(l.st.ids))
	}
	id := c.ID()
	l.st.ids[idx].Store(uint64(id.Gen)<<32 | uint64(id.Slot))

	switch l.workload {
	case "group":
		room := fmt.Sprintf("g%d", idx%uint32(l.groups))
		if err := c.Join(room); err != nil {
			return err
		}
	case "churn":
		if err := c.Join("churn"); err != nil {
			return err
		}
	}

	var b [4]byte
	binary.BigEndian.PutUint32(b[:], idx)
	return c.EmitID(evWelcome, b[:])
}

func (*lifecycle) OnMessage(c *encryl.Conn, m encryl.Message) error {
	return nil
}

func (*lifecycle) OnClose(c *encryl.Conn, info encryl.CloseInfo) {}

// ---------------------------------------------------------------- client

type histogram struct {
	buckets []atomic.Uint64
	count   atomic.Uint64
	maxMs   atomic.Uint64
}

func newHistogram() *histogram { return &histogram{buckets: make([]atomic.Uint64, 60_000)} }

func (h *histogram) observe(d time.Duration) {
	ms := uint64(d.Milliseconds())
	if ms >= uint64(len(h.buckets)) {
		ms = uint64(len(h.buckets) - 1)
	}
	h.buckets[ms].Add(1)
	h.count.Add(1)
	for {
		old := h.maxMs.Load()
		if ms <= old || h.maxMs.CompareAndSwap(old, ms) {
			break
		}
	}
}

func (h *histogram) percentile(q float64) uint64 {
	total := h.count.Load()
	if total == 0 {
		return 0
	}
	want := uint64(q * float64(total-1))
	var seen uint64
	for i := range h.buckets {
		seen += h.buckets[i].Load()
		if seen > want {
			return uint64(i)
		}
	}
	return uint64(len(h.buckets) - 1)
}

func runClient(addr, workload string, conns, rate int, duration time.Duration,
	roomSize, srcIPs, msgSize, workers int, churnGap time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	host := addr
	u := "ws://" + host + "/ws"
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Subprotocols:     []string{"encryl.v2"},
	}

	var (
		sent, recv, failed atomic.Int64
		hist               = newHistogram()
	)
	start := time.Now()

	report := func() {
		n := hist.count.Load()
		log.Printf("sent=%d recv=%d failed=%d p50=%dms p95=%dms p99=%dms p99.9=%dms p99.99=%dms max=%dms rate=%.0f/s",
			sent.Load(), recv.Load(), failed.Load(),
			hist.percentile(0.50), hist.percentile(0.95), hist.percentile(0.99),
			hist.percentile(0.999), hist.percentile(0.9999), hist.maxMs.Load(),
			float64(n)/time.Since(start).Seconds())
	}
	if workload != "churn" {
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					report()
				}
			}
		}()
	}

	if workload == "churn" {
		runChurn(ctx, &dialer, u, workers, churnGap, &sent, &failed)
	} else {
		var wg sync.WaitGroup
		// Bound concurrent dials so a 100k ramp does not overflow the
		// listener backlog. The slot is released after the handshake.
		sem := make(chan struct{}, 4096)
		for i := 0; i < conns; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				srcIP := ""
				if srcIPs > 1 {
					srcIP = fmt.Sprintf("127.0.0.%d", 1+i%srcIPs)
				}
				sem <- struct{}{}
				c, idx, err := dialClient(&dialer, u, srcIP)
				<-sem
				if err != nil {
					failed.Add(1)
					return
				}
				defer func() { _ = c.Close() }()

				switch workload {
				case "unicast":
					unicastClient(ctx, c, i, conns, rate, msgSize, idx, hist, &sent, &recv)
				case "group":
					groupClient(ctx, c, idx, conns, roomSize, rate, msgSize, hist, &sent, &recv)
				case "ack":
					ackClient(ctx, c, rate, conns, msgSize, hist, &sent, &recv)
				}
			}(i)
		}
		wg.Wait()
	}
	report()
}

func dialClient(d *websocket.Dialer, u, srcIP string) (*websocket.Conn, uint32, error) {
	dd := *d
	if srcIP != "" {
		dd.NetDial = func(network, address string) (net.Conn, error) {
			return (&net.Dialer{Timeout: d.HandshakeTimeout,
				LocalAddr: &net.TCPAddr{IP: parseIP(srcIP)}}).Dial(network, address)
		}
	}
	c, resp, err := dd.Dial(u, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, 0, err
	}
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		_ = c.Close()
		return nil, 0, err
	}
	if len(data) < 16+4 {
		_ = c.Close()
		return nil, 0, fmt.Errorf("short welcome")
	}
	idx := binary.BigEndian.Uint32(data[16 : 16+4])
	_ = c.SetReadDeadline(time.Time{})
	return c, idx, nil
}

func unicastClient(ctx context.Context, c *websocket.Conn, self, conns, rate, msgSize int,
	idx uint32, hist *histogram, sent, recv *atomic.Int64) {
	go readLoop(ctx, c, hist, recv)
	interval := time.Duration(float64(conns) / float64(rate) * float64(time.Second))
	if interval <= 0 {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	buf := make([]byte, 12+msgSize)
	binary.BigEndian.PutUint32(buf[0:4], uint32(rand.Intn(conns)))
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			binary.BigEndian.PutUint64(buf[4:12], uint64(time.Now().UnixNano()))
			if err := c.WriteMessage(websocket.BinaryMessage, envelope(evUnicast, buf)); err != nil {
				return
			}
			sent.Add(1)
		}
	}
}

func groupClient(ctx context.Context, c *websocket.Conn, idx uint32, conns, roomSize, rate, msgSize int,
	hist *histogram, sent, recv *atomic.Int64) {
	go readLoop(ctx, c, hist, recv)
	// One publisher per room. Each room has roomSize members, so each
	// publisher sends at rate/rooms and every member receives it.
	if int(idx)%roomSize != 0 {
		<-ctx.Done()
		return
	}
	// The server derives the room from idx%groups. Publishers share the
	// aggregate rate equally: publishers = conns/roomSize.
	publishers := conns / roomSize
	if publishers < 1 {
		publishers = 1
	}
	interval := time.Duration(float64(publishers) / float64(rate) * float64(time.Second))
	if interval <= 0 {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	buf := make([]byte, 12+msgSize)
	binary.BigEndian.PutUint32(buf[0:4], idx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			binary.BigEndian.PutUint64(buf[4:12], uint64(time.Now().UnixNano()))
			if err := c.WriteMessage(websocket.BinaryMessage, envelope(evGroup, buf)); err != nil {
				return
			}
			sent.Add(1)
		}
	}
}

func ackClient(ctx context.Context, c *websocket.Conn, rate, conns, msgSize int,
	hist *histogram, sent, recv *atomic.Int64) {
	interval := time.Duration(float64(conns) / float64(rate) * float64(time.Second))
	if interval <= 0 {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	buf := make([]byte, msgSize)
	reqID := uint64(1)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			start := time.Now()
			msg := envelopeAck(evAck, reqID, buf)
			if err := c.WriteMessage(websocket.BinaryMessage, msg); err != nil {
				return
			}
			sent.Add(1)
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if len(data) >= 16 && data[1] == 3 { // ACK kind
				hist.observe(time.Since(start))
				recv.Add(1)
			}
			reqID++
		}
	}
}

func readLoop(ctx context.Context, c *websocket.Conn, hist *histogram, recv *atomic.Int64) {
	for ctx.Err() == nil {
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		if len(data) < 16+8 {
			continue
		}
		ts := int64(binary.BigEndian.Uint64(data[16:24]))
		hist.observe(time.Since(time.Unix(0, ts)))
		recv.Add(1)
	}
}

func runChurn(ctx context.Context, d *websocket.Dialer, u string, workers int,
	gap time.Duration, sent, failed *atomic.Int64) {
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			srcIP := fmt.Sprintf("127.0.0.%d", 1+w%40)
			for ctx.Err() == nil {
				c, _, err := dialClient(d, u, srcIP)
				if err != nil {
					failed.Add(1)
					continue
				}
				buf := make([]byte, 16)
				binary.BigEndian.PutUint64(buf[0:8], uint64(time.Now().UnixNano()))
				_ = c.WriteMessage(websocket.BinaryMessage, envelope(evUnicast, buf))
				_ = c.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(1000, ""), time.Now().Add(time.Second))
				_ = c.Close()
				sent.Add(1)
				select {
				case <-ctx.Done():
					return
				case <-time.After(gap):
				}
			}
		}(w)
	}
	wg.Wait()
}

func envelope(eventID uint32, payload []byte) []byte {
	out := make([]byte, 16+len(payload))
	out[0] = 2
	out[1] = 2 // EVENT
	binary.BigEndian.PutUint32(out[4:8], eventID)
	copy(out[16:], payload)
	return out
}

func envelopeAck(eventID uint32, requestID uint64, payload []byte) []byte {
	out := make([]byte, 16+len(payload))
	out[0] = 2
	out[1] = 2 // EVENT
	binary.BigEndian.PutUint32(out[4:8], eventID)
	binary.BigEndian.PutUint64(out[8:16], requestID)
	copy(out[16:], payload)
	return out
}

func parseIP(s string) net.IP {
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	return ip
}
