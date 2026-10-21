//go:build linux

// Command fanout is the Gate C harness: every connection joins one room, and
// the server broadcasts one event per interval to the whole room.
//
// The payload carries an 8-byte sequence number and an 8-byte send timestamp.
// The harness records the server-side latency of each broadcast: the time from
// Emit to the last socket write completion. It reads one process clock, so the
// measurement has no clock skew.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl"
	"github.com/encryptedbusiness/encryl/event"
	"github.com/encryptedbusiness/encryl/ws"
)

const (
	eventJoin      = 1024
	eventBroadcast = 1025
	payloadSize    = 16
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18082", "listen address")
	interval := flag.Duration("interval", time.Second, "broadcast interval")
	maxConns := flag.Int("max-conns", 20000, "connection ceiling")
	count := flag.Int("count", 0, "number of broadcasts; 0 runs until stopped")
	rooms := flag.Int("rooms", 1, "number of rooms")
	roomSize := flag.Int("room-size", 100, "connections per room")
	loops := flag.Int("loops", 0, "event loops; 0 uses NumCPU")
	autoStart := flag.Bool("auto-start", true, "start broadcasting at once")
	settle := flag.Duration("settle", 2*time.Second, "quiet time before the first broadcast")
	flag.Parse()

	cfg := encryl.DefaultConfig()
	cfg.Addr = *addr
	cfg.AllowedOrigins = []string{"*"}
	cfg.MaxConnections = *maxConns
	cfg.Loops = *loops
	cfg.MaxRoomsPerConn = 8
	// Readers are idle. Disable pings and the idle close so they do not add
	// bytes to the completion counter or drop connections during a long hold.
	cfg.PingInterval = 0
	cfg.IdleTimeout = 24 * time.Hour

	srv, err := encryl.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.SetHandler(&lifecycle{roomSize: uint32(*roomSize)}); err != nil {
		log.Fatal(err)
	}
	if err := srv.OnID(eventJoin, func(c *encryl.Conn, m encryl.Message) error {
		return c.Join(string(m.Payload))
	}); err != nil {
		log.Fatal(err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	go func() {
		for range sig {
			log.Printf("stats: %+v room=%d", srv.Stats(), srv.RoomSize("room"))
		}
	}()

	go broadcastLoop(srv, *interval, *count, *rooms, *roomSize, *autoStart, *settle)

	log.Printf("fanout listening on %s; rooms=%d room_size=%d", *addr, *rooms, *roomSize)
	log.Fatal(srv.ListenAndServe(context.Background()))
}

// frameBytes is the wire size of one broadcast frame: WebSocket header plus
// envelope plus payload.
var frameBytes = uint64(ws.FrameHeaderLen(event.HeaderSize+payloadSize) + event.HeaderSize + payloadSize)

func broadcastLoop(srv *encryl.Server, interval time.Duration, count, rooms, roomSize int, autoStart bool, settle time.Duration) {
	if !autoStart {
		start := make(chan os.Signal, 1)
		signal.Notify(start, syscall.SIGUSR1)
		<-start
	} else {
		time.Sleep(settle)
	}

	payload := make([]byte, payloadSize)
	latencies := make([]time.Duration, 0, max(count, 64))
	start := time.Now()

	for seq := 1; count == 0 || seq <= count; seq++ {
		target := start.Add(time.Duration(seq) * interval)
		if wait := time.Until(target); wait > 0 {
			time.Sleep(wait)
		}

		room := fmt.Sprintf("r%d", (seq-1)%rooms)
		members := srv.RoomSize(room)
		if members == 0 {
			log.Printf("seq=%d room %s is empty; skip", seq, room)
			continue
		}
		binary.BigEndian.PutUint64(payload[0:8], uint64(seq))
		binary.BigEndian.PutUint64(payload[8:16], uint64(time.Now().UnixNano()))

		before := srv.BytesOut()
		enqueued := time.Now()
		if err := srv.To(room).EmitID(eventBroadcast, payload); err != nil {
			log.Printf("seq=%d emit: %v", seq, err)
			continue
		}
		done, ok := waitBytes(srv, before, uint64(members)*frameBytes, 30*time.Second)
		if !ok {
			log.Printf("seq=%d completion timeout: members=%d bytes=%d/%d",
				seq, members, srv.BytesOut()-before, uint64(members)*frameBytes)
			continue
		}
		lat := done.Sub(enqueued)
		latencies = append(latencies, lat)
		log.Printf("seq=%d room=%s members=%d latency=%s", seq, room, members, lat.Round(time.Microsecond))
	}

	report(latencies)
}

// waitBytes waits until the byte counter reaches want, or stops moving for a
// quiet period. The quiet period handles a recipient that closed during the
// broadcast and will never receive its frame.
func waitBytes(srv *encryl.Server, before, want uint64, timeout time.Duration) (time.Time, bool) {
	deadline := time.Now().Add(timeout)
	last := before
	lastMove := time.Now()
	for {
		now := srv.BytesOut()
		if now-before >= want {
			return time.Now(), true
		}
		if now != last {
			last = now
			lastMove = time.Now()
		} else if time.Since(lastMove) > 5*time.Millisecond {
			return time.Now(), true
		}
		if time.Now().After(deadline) {
			return time.Now(), false
		}
		time.Sleep(100 * time.Microsecond)
	}
}

func report(samples []time.Duration) {
	if len(samples) == 0 {
		log.Printf("no completed broadcasts")
		return
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p := func(q float64) time.Duration {
		i := int(q * float64(len(samples)-1))
		return samples[i]
	}
	log.Printf("broadcasts=%d p50=%s p95=%s p99=%s p99.9=%s p99.99=%s max=%s",
		len(samples), p(0.50).Round(time.Microsecond), p(0.95).Round(time.Microsecond),
		p(0.99).Round(time.Microsecond), p(0.999).Round(time.Microsecond),
		p(0.9999).Round(time.Microsecond), samples[len(samples)-1].Round(time.Microsecond))
}

type lifecycle struct {
	next     atomic.Uint32
	roomSize uint32
}

func (l *lifecycle) OnOpen(c *encryl.Conn) error {
	idx := l.next.Add(1) - 1
	return c.Join(fmt.Sprintf("r%d", idx/l.roomSize))
}
func (*lifecycle) OnMessage(c *encryl.Conn, m encryl.Message) error {
	return c.EmitID(m.Event, m.Payload)
}
func (*lifecycle) OnClose(c *encryl.Conn, info encryl.CloseInfo) {}
