//go:build linux

// Command loadgen is the reproducible load generator used for every number in
// docs/benchmarks. It replaces benchmark/Benchmark.go from v1, which had data
// races (loop-variable capture, non-atomic progress reads), no percentiles, and
// no way to reproduce a run.
//
// Modes:
//
//	idle    connect and hold. The density test.
//	echo    write a message, wait for the echo, record RTT.
//	read    connect and only read pushed messages (fan-out receivers).
//
// For more than ~100k connections, run several processes with -shard/-shards
// and spread source addresses with -src-ips. One goroutine per connection is
// deliberate: the generator must not be the bottleneck being measured, and a
// goroutine per client is the realistic browser model.
package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/csv"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	var (
		addr     = flag.String("addr", "127.0.0.1:8080", "host:port")
		path     = flag.String("path", "/ws", "upgrade path")
		scheme   = flag.String("scheme", "ws", "ws or wss")
		conns    = flag.Int("conns", 10_000, "total connections for this shard")
		rate     = flag.Int("rate", 1000, "connections per second")
		mode     = flag.String("mode", "idle", "idle|echo|read")
		msgSize  = flag.Int("msg-size", 256, "payload size for echo mode")
		duration = flag.Duration("duration", 60*time.Second, "run time")
		srcIPs   = flag.Int("src-ips", 1, "spread over 127.0.0.N local addresses")
		shard    = flag.Int("shard", 0, "shard index (0-based)")
		shards   = flag.Int("shards", 1, "total shards")
		insecure = flag.Bool("insecure", false, "skip TLS verification")
		envelope = flag.Bool("envelope", false, "wrap echo payload in the encryl.v2 header")
		msgRate  = flag.Float64("msg-rate", 0, "messages per second per connection in echo mode; 0 sends as fast as possible")
		report   = flag.String("report", "", "write raw results to this CSV path")
		timeout  = flag.Duration("timeout", 15*time.Second, "dial timeout")
		ping     = flag.Duration("ping", 30*time.Second, "ping interval in idle mode")
	)
	flag.Parse()

	if *shard < 0 || *shard >= *shards {
		fmt.Fprintln(os.Stderr, "loadgen: shard must be in [0, shards)")
		os.Exit(2)
	}
	if *msgSize < 1 || *msgSize > 1<<20 {
		fmt.Fprintln(os.Stderr, "loadgen: msg-size must be in [1, 1MiB]")
		os.Exit(2)
	}

	u := url.URL{Scheme: *scheme, Host: *addr, Path: *path}
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The deadline must cancel the context, not only stop spawning. Otherwise
	// the final <-ctx.Done() blocks forever after a run completes.
	ctx, cancel := context.WithTimeout(sigCtx, *duration)
	defer cancel()

	hist := newHistogram()
	var (
		attempted atomic.Int64
		active    atomic.Int64
		peak      atomic.Int64
		failed    atomic.Int64
		messages  atomic.Int64
		bytesIn   atomic.Int64
	)

	dialer := websocket.Dialer{
		HandshakeTimeout: *timeout,
		TLSClientConfig:  insecureTLSConfig(*insecure),
		Subprotocols:     []string{"encryl.v2"},
	}
	payload := make([]byte, *msgSize)
	wire := payload
	if *envelope {
		wire = make([]byte, 16+len(payload))
		wire[0] = 2
		wire[1] = 2 // EVENT
		binary.BigEndian.PutUint32(wire[4:8], 1024)
		copy(wire[16:], payload)
	}
	start := time.Now()
	deadline := start.Add(*duration)

	perShard := *conns / *shards
	if *shard == *shards-1 {
		perShard += *conns % *shards
	}
	interval := time.Duration(0)
	if *rate > 0 {
		interval = time.Second / time.Duration(*rate)
	}
	next := start

	var wg sync.WaitGroup
	sem := make(chan struct{}, 4096)

	reportProgress := func() {
		cur := active.Load()
		if cur > peak.Load() {
			peak.Store(cur)
		}
	}

	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				reportProgress()
				fmt.Printf("[%s] attempted=%d active=%d peak=%d failed=%d msg=%d\n",
					time.Since(start).Round(time.Second), attempted.Load(), active.Load(),
					peak.Load(), failed.Load(), messages.Load())
			}
		}
	}()

spawn:
	for i := 0; i < perShard; i++ {
		if ctx.Err() != nil || time.Now().After(deadline) {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break spawn
		}

		srcIP := ""
		if *srcIPs > 1 {
			// Shards use disjoint source addresses so the server sees 2 x
			// srcIPs distinct peers instead of 2 x the same range.
			srcIP = fmt.Sprintf("127.0.0.%d", 1+*shard**srcIPs+(i%*srcIPs))
		}

		wg.Add(1)
		go func(localIP string) {
			defer wg.Done()

			attempted.Add(1)
			conn, err := dial(&dialer, u.String(), localIP)
			// The semaphore bounds concurrent dials, not the number of live
			// connections. Holding it for the connection's lifetime would cap
			// the run at the channel capacity.
			<-sem
			if err != nil {
				failed.Add(1)
				return
			}
			active.Add(1)
			defer func() {
				active.Add(-1)
				_ = conn.Close()
			}()

			switch *mode {
			case "echo":
				echoLoop(ctx, conn, wire, hist, &messages, &bytesIn, *msgRate)
			case "read":
				readLoop(ctx, conn, &messages, &bytesIn)
			default:
				idleLoop(ctx, conn, *ping)
			}
		}(srcIP)

		// Pace with an absolute schedule. Sleeping `interval` per iteration is
		// capped at roughly one connection per timer tick (~1 ms), which makes
		// any rate above ~1000/s unreachable. Accumulating the schedule means
		// we only wait when we are ahead of it, and catch up in bursts when we
		// are behind.
		if interval > 0 {
			next = next.Add(interval)
			for {
				wait := time.Until(next)
				if wait <= 0 {
					break
				}
				if wait > 2*time.Millisecond {
					wait = 2 * time.Millisecond
				}
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					break spawn
				}
			}
		}
	}

	<-ctx.Done()
	// Give in-flight dials a moment to observe cancellation, then stop
	// waiting on connections that are blocked in a read.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}

	// The periodic sampler may never fire on short runs.
	reportProgress()

	elapsed := time.Since(start)
	snap := hist.snapshot()
	fmt.Printf("\n=== summary (shard %d/%d, mode=%s) ===\n", *shard, *shards, *mode)
	fmt.Printf("duration       %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("attempted      %d\n", attempted.Load())
	fmt.Printf("active_peak    %d\n", peak.Load())
	fmt.Printf("failed         %d\n", failed.Load())
	fmt.Printf("messages       %d (%.0f/s)\n", messages.Load(), float64(messages.Load())/elapsed.Seconds())
	fmt.Printf("bytes_in       %d (%.1f MiB/s)\n", bytesIn.Load(), float64(bytesIn.Load())/elapsed.Seconds()/(1<<20))
	if snap.count > 0 {
		fmt.Printf("rtt_us         p50=%d p95=%d p99=%d p999=%d max=%d n=%d\n",
			snap.p50.Microseconds(), snap.p95.Microseconds(), snap.p99.Microseconds(),
			snap.p999.Microseconds(), snap.max.Microseconds(), snap.count)
	}

	if *report != "" {
		if err := writeCSV(*report, snap, attempted.Load(), peak.Load(), failed.Load(),
			messages.Load(), bytesIn.Load(), elapsed); err != nil {
			fmt.Fprintln(os.Stderr, "loadgen: report:", err)
			os.Exit(1)
		}
	}
}

func dial(d *websocket.Dialer, target, localIP string) (*websocket.Conn, error) {
	if localIP == "" {
		conn, resp, err := d.Dial(target, http.Header{})
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return conn, err
	}

	// Clone per dial: the v1 generator captured the loop variable and every
	// connection ended up on the last source address.
	dd := *d
	dd.NetDial = func(network, address string) (net.Conn, error) {
		nd := net.Dialer{
			Timeout:   d.HandshakeTimeout,
			LocalAddr: &net.TCPAddr{IP: net.ParseIP(localIP)},
		}
		return nd.Dial(network, address)
	}
	conn, resp, err := dd.Dial(target, http.Header{})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return conn, err
}

func idleLoop(ctx context.Context, conn *websocket.Conn, ping time.Duration) {
	if ping <= 0 {
		<-ctx.Done()
		return
	}
	t := time.NewTicker(ping)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
}

func echoLoop(ctx context.Context, conn *websocket.Conn, payload []byte,
	hist *histogram, messages, bytesIn *atomic.Int64, msgRate float64) {
	var interval time.Duration
	var timer *time.Timer
	if msgRate > 0 {
		interval = time.Duration(float64(time.Second) / msgRate)
		timer = time.NewTimer(interval)
		if !timer.Stop() {
			<-timer.C
		}
		defer timer.Stop()
	}
	next := time.Now()
	for ctx.Err() == nil {
		if interval > 0 {
			// Absolute schedule: do not drift, and do not burst after a stall.
			next = next.Add(interval)
			wait := time.Until(next)
			if wait < -interval {
				next = time.Now()
				wait = 0
			}
			if wait > 0 {
				timer.Reset(wait)
				select {
				case <-timer.C:
				case <-ctx.Done():
					return
				}
			}
		}
		sent := time.Now()
		if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		hist.observe(time.Since(sent))
		messages.Add(1)
		bytesIn.Add(int64(len(data)))
	}
}

func readLoop(ctx context.Context, conn *websocket.Conn, messages, bytesIn *atomic.Int64) {
	for ctx.Err() == nil {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		messages.Add(1)
		bytesIn.Add(int64(len(data)))
	}
}

func insecureTLSConfig(insecure bool) *tls.Config {
	if !insecure {
		return nil
	}
	return &tls.Config{InsecureSkipVerify: true}
}

// histogram is a fixed-resolution latency histogram: 1 ms buckets up to 60 s,
// which is enough resolution for queue and end-to-end latency without storing
// every sample.
type histogram struct {
	mu      sync.Mutex
	buckets []uint64
	count   uint64
	max     time.Duration
}

func newHistogram() *histogram {
	return &histogram{buckets: make([]uint64, 60_000)}
}

func (h *histogram) observe(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	idx := int(d / time.Millisecond)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(h.buckets) {
		idx = len(h.buckets) - 1
	}
	h.buckets[idx]++
	h.count++
	if d > h.max {
		h.max = d
	}
}

type snapshot struct {
	p50, p95, p99, p999, max time.Duration
	count                    uint64
}

func (h *histogram) snapshot() snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	var out snapshot
	out.count = h.count
	out.max = h.max
	if h.count == 0 {
		return out
	}
	targets := []struct {
		q  float64
		at *time.Duration
	}{
		{0.50, &out.p50}, {0.95, &out.p95}, {0.99, &out.p99}, {0.999, &out.p999},
	}
	var cumulative uint64
	next := 0
	for i, c := range h.buckets {
		cumulative += c
		for next < len(targets) && float64(cumulative) >= targets[next].q*float64(h.count) {
			*targets[next].at = time.Duration(i) * time.Millisecond
			next++
		}
		if next == len(targets) {
			break
		}
	}
	return out
}

func writeCSV(path string, s snapshot, attempted, peak, failed, messages, bytesIn int64, elapsed time.Duration) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{
		"metric", "value",
	}); err != nil {
		return err
	}
	rows := [][]string{
		{"duration_seconds", fmt.Sprintf("%.3f", elapsed.Seconds())},
		{"attempted", fmt.Sprint(attempted)},
		{"active_peak", fmt.Sprint(peak)},
		{"failed", fmt.Sprint(failed)},
		{"messages", fmt.Sprint(messages)},
		{"bytes_in", fmt.Sprint(bytesIn)},
		{"rtt_p50_us", fmt.Sprint(s.p50.Microseconds())},
		{"rtt_p95_us", fmt.Sprint(s.p95.Microseconds())},
		{"rtt_p99_us", fmt.Sprint(s.p99.Microseconds())},
		{"rtt_p999_us", fmt.Sprint(s.p999.Microseconds())},
		{"rtt_max_us", fmt.Sprint(s.max.Microseconds())},
		{"rtt_samples", fmt.Sprint(s.count)},
	}
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return w.Error()
}
