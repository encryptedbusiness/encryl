//go:build linux

package httpuring

import (
	"bytes"
	"io"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/encryptedbusiness/encryl/uring"
	"golang.org/x/sys/unix"
)

// newArenaLoop builds a loop with one small RX/TX arena and one connection
// slot whose descriptor is -1, so close paths never touch a real socket.
func newArenaLoop(t *testing.T, blockBytes, blocks int) (*fastLoop, *fastConn, *uring.RXArena, *uring.TXArena) {
	t.Helper()
	rx, err := uring.NewRXArena(blockBytes, blocks)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rx.Close() })
	tx, err := uring.NewTXArena(int64(blockBytes*blocks), blockBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	f := &fastLoop{server: &Server{MaxOutputBytes: 1 << 20}, rx: rx, tx: tx}
	_, c := f.slots.alloc(-1)
	return f, c, rx, tx
}

func TestCloseDefersInflightRXBuffer(t *testing.T) {
	f, c, rx, _ := newArenaLoop(t, 64, 1)
	id, gen := c.id, c.gen

	bid, ok := rx.Take()
	if !ok {
		t.Fatal("take rx")
	}
	c.rxBid = bid
	c.set(flagRecv)
	f.closeConn(c)

	if rx.InUse() != 1 {
		t.Fatalf("rx buffer recycled with a receive outstanding: inUse=%d", rx.InUse())
	}
	if f.slots.live != 1 {
		t.Fatalf("slot released with a receive outstanding: live=%d", f.slots.live)
	}

	f.handleRecv(id, gen, 0)

	if rx.InUse() != 0 {
		t.Fatalf("rx buffer not released after completion: inUse=%d", rx.InUse())
	}
	if f.slots.live != 0 {
		t.Fatalf("slot not released after completion: live=%d", f.slots.live)
	}
	if !rx.Balance() {
		t.Fatal("rx arena accounting is inconsistent")
	}
}

func TestCloseDefersInflightTXBlock(t *testing.T) {
	f, c, _, tx := newArenaLoop(t, 64, 1)
	id, gen := c.id, c.gen

	bid, ok := tx.Take(id, gen, 16)
	if !ok {
		t.Fatal("take tx")
	}
	c.txBid = bid
	c.txLen = 16
	c.set(flagSend)
	f.closeConn(c)

	if tx.InUse() != 1 {
		t.Fatalf("tx block recycled with a send outstanding: inUse=%d", tx.InUse())
	}
	if f.slots.live != 1 {
		t.Fatalf("slot released with a send outstanding: live=%d", f.slots.live)
	}

	f.handleSend(id, gen, 0)

	if tx.InUse() != 0 {
		t.Fatalf("tx block not released after completion: inUse=%d", tx.InUse())
	}
	if f.slots.live != 0 {
		t.Fatalf("slot not released after completion: live=%d", f.slots.live)
	}
}

func TestCloseWaitsForBothDirections(t *testing.T) {
	f, c, rx, tx := newArenaLoop(t, 64, 1)
	id, gen := c.id, c.gen

	rxBid, _ := rx.Take()
	txBid, _ := tx.Take(id, gen, 8)
	c.rxBid = rxBid
	c.txBid = txBid
	c.txLen = 8
	c.set(flagRecv)
	c.set(flagSend)
	f.closeConn(c)

	f.handleRecv(id, gen, int32(0))
	if f.slots.live != 1 || rx.InUse() != 0 || tx.InUse() != 1 {
		t.Fatalf("released too early: live=%d rx=%d tx=%d", f.slots.live, rx.InUse(), tx.InUse())
	}
	f.handleSend(id, gen, 0)
	if f.slots.live != 0 || tx.InUse() != 0 {
		t.Fatalf("not released after both completions: live=%d tx=%d", f.slots.live, tx.InUse())
	}
}

func TestReleaseRefusesInflightBuffer(t *testing.T) {
	f, c, rx, tx := newArenaLoop(t, 64, 1)
	bid, _ := rx.Take()
	c.rxBid = bid
	c.set(flagRecv)
	f.releaseRX(c)
	if c.rxBid != bid || rx.InUse() != 1 {
		t.Fatalf("releaseRX freed an in-flight buffer: bid=%d inUse=%d", c.rxBid, rx.InUse())
	}
	c.clear(flagRecv)

	txBid, _ := tx.Take(c.id, c.gen, 8)
	c.txBid = txBid
	c.set(flagSend)
	f.releaseTX(c)
	if c.txBid != txBid || tx.InUse() != 1 {
		t.Fatalf("releaseTX freed an in-flight block: bid=%d inUse=%d", c.txBid, tx.InUse())
	}
}

func TestPendingSendIsBounded(t *testing.T) {
	f, c, _, tx := newArenaLoop(t, 64, 1)
	f.server.MaxOutputBytes = 100
	// Starve the arena from another owner so this connection must park.
	if _, ok := tx.Take(999, 999, 16); !ok {
		t.Fatal("take tx")
	}
	a := bytes.Repeat([]byte("a"), 60)
	b := bytes.Repeat([]byte("b"), 60)
	f.beginSend(c, a)
	if !c.has(flagParkedSend) {
		t.Fatal("first response did not park")
	}
	f.beginSend(c, b)
	if c.has(flagLive) {
		t.Fatal("second response exceeded the pending cap but connection stayed live")
	}
	if f.slots.live != 0 {
		t.Fatalf("closed connection slot not released: live=%d", f.slots.live)
	}
}

func TestProcessRequestsStopsWhileSendParked(t *testing.T) {
	mux := &Mux{}
	calls := 0
	mux.HandleFunc("/x", func(req *Request, res *Response) {
		calls++
		res.Body = []byte("ok")
	})
	f, c := newParseHarness(t, mux)
	req := []byte("GET /x HTTP/1.1\r\nHost: x\r\n\r\nGET /x HTTP/1.1\r\nHost: x\r\n\r\n")
	buf := f.rx.Bytes(c.rxBid)
	copy(buf, req)
	c.rxEnd = int32(len(req))
	c.set(flagParkedSend)

	f.processRequests(c)

	if calls != 0 {
		t.Fatalf("processed %d requests while the send path was parked", calls)
	}
	if c.rxStart != 0 {
		t.Fatalf("consumed %d bytes while parked", c.rxStart)
	}
}

func TestSlotTableReclaimsChunks(t *testing.T) {
	var slots slotTable
	ids := make([]uint32, 600)
	for i := range ids {
		ids[i], _ = slots.alloc(-1)
	}
	if len(slots.chunks) != 3 || slots.live != 600 {
		t.Fatalf("chunks=%d live=%d", len(slots.chunks), slots.live)
	}
	for _, id := range ids {
		slots.release(id)
	}
	if slots.live != 0 || len(slots.chunks) != 0 || slots.next != 0 || len(slots.free) != 0 {
		t.Fatalf("slab not reclaimed: live=%d chunks=%d next=%d free=%d",
			slots.live, len(slots.chunks), slots.next, len(slots.free))
	}
}

func TestSlotChunksInitializeUnusedDescriptors(t *testing.T) {
	var slots slotTable
	id, c := slots.alloc(5)
	if c.fd != 5 {
		t.Fatalf("allocated fd = %d", c.fd)
	}
	for i := range slots.chunks[0] {
		if uint32(i) == id {
			continue
		}
		if fd := slots.chunks[0][i].fd; fd != -1 {
			t.Fatalf("unused slot %d has fd %d", i, fd)
		}
	}
	slots.release(id)
}

func TestAcceptResourceErrorsBackOff(t *testing.T) {
	f := &fastLoop{start: time.Now()}
	f.now = 10
	f.handleAcceptError(unix.EMFILE)
	if f.acceptBackoff != 100 || f.acceptRetryAt != 110 {
		t.Fatalf("first backoff = %d retryAt=%d", f.acceptBackoff, f.acceptRetryAt)
	}
	f.now = 20
	f.handleAcceptError(unix.ENFILE)
	if f.acceptBackoff != 200 || f.acceptRetryAt != 220 {
		t.Fatalf("second backoff = %d retryAt=%d", f.acceptBackoff, f.acceptRetryAt)
	}
	f.handleAcceptError(unix.ECONNABORTED)
	if f.acceptRetryAt != 0 {
		t.Fatalf("transient accept error left retryAt=%d", f.acceptRetryAt)
	}

	f.acceptBackoff = 0
	f.now = 30
	f.handleAcceptError(unix.EPERM)
	if f.stop.Load() || f.acceptBackoff != 100 || f.acceptRetryAt != 130 {
		t.Fatalf("EPERM stopped=%v backoff=%d retryAt=%d", f.stop.Load(), f.acceptBackoff, f.acceptRetryAt)
	}
	f.handleAcceptError(unix.ECONNRESET)
	if f.stop.Load() || f.acceptRetryAt == 0 {
		t.Fatalf("pending network error stopped=%v retryAt=%d", f.stop.Load(), f.acceptRetryAt)
	}
	f.handleAcceptError(unix.EBADF)
	if !f.stop.Load() {
		t.Fatal("listener-gone error did not stop the loop")
	}
}

func TestCloseDefersSlotUntilPollCompletion(t *testing.T) {
	f, c, _, _ := newArenaLoop(t, 64, 1)
	id, gen := c.id, c.gen
	c.set(flagPoll)

	f.closeConn(c)
	if f.slots.live != 1 {
		t.Fatalf("slot released with a poll outstanding: live=%d", f.slots.live)
	}
	f.handlePoll(id, gen, int32(unix.POLLHUP))
	if f.slots.live != 0 {
		t.Fatalf("slot not released after poll completion: live=%d", f.slots.live)
	}
}

func TestTryReleaseWaitsForDescriptorClose(t *testing.T) {
	f, c, _, _ := newArenaLoop(t, 64, 1)
	c.clear(flagLive)
	c.fd = 7
	f.tryRelease(c, c.gen)
	if f.slots.live != 1 {
		t.Fatalf("released with an open descriptor: live=%d", f.slots.live)
	}
	c.fd = -1
	f.tryRelease(c, c.gen)
	if f.slots.live != 0 {
		t.Fatalf("not released after descriptor close: live=%d", f.slots.live)
	}
}

func TestCloseFlushesPreparedSend(t *testing.T) {
	// The ring enforces SINGLE_ISSUER, so creation and submission must run on
	// the same OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ring, err := uring.NewRing(64)
	if err != nil {
		t.Fatal(err)
	}
	defer ring.QueueExit()
	tx, err := uring.NewTXArena(64, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	f := &fastLoop{server: &Server{MaxOutputBytes: 1 << 20}, ring: ring, tx: tx}

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, c := f.slots.alloc(fds[0])
	c.txHeap = []byte("hello")
	c.txLen = int32(len(c.txHeap))
	f.submitSend(c)
	if !c.has(flagSend) {
		t.Fatal("send was not prepared")
	}

	// closeConn must submit the prepared send while the descriptor is still
	// open, then close it.
	f.closeConn(c)
	if _, err := ring.SubmitAndWait(1); err != nil {
		t.Fatal(err)
	}
	cqe, err := ring.PeekCQE()
	if err != nil {
		t.Fatal(err)
	}
	if cqe.Res != int32(len(c.txHeap)) {
		t.Fatalf("prepared send completed with %d, want %d", cqe.Res, len(c.txHeap))
	}
	ring.CQESeen(cqe)

	file := os.NewFile(uintptr(fds[1]), "peer")
	peer, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, len("hello"))
	if _, err := io.ReadFull(peer, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("peer received %q", buf)
	}
}

func TestHijackRejectsInflightIO(t *testing.T) {
	f := &fastLoop{server: &Server{}}
	_, c := f.slots.alloc(-1)
	c.set(flagSend)
	w := &httpResponseWriter{job: &httpJob{c: c, gen: c.gen, loop: f}}
	if _, _, err := w.Hijack(); err == nil {
		t.Fatal("hijack succeeded with a send outstanding")
	}
}

func TestCompleteHTTPReleasesSlotAfterClose(t *testing.T) {
	f, c, _, _ := newArenaLoop(t, 64, 1)
	f.jobs = newHTTPJobPool()
	job := f.jobs.get()
	job.c = c
	job.gen = c.gen
	job.loop = f
	if c.extra == nil {
		c.extra = &httpExtra{}
	}
	c.extra.job = job
	c.set(flagHTTPPending)

	f.closeConn(c)
	if f.slots.live != 1 {
		t.Fatalf("slot released while an HTTP job was in flight: live=%d", f.slots.live)
	}
	f.completeHTTP(job)
	if f.slots.live != 0 {
		t.Fatalf("slot leaked after the HTTP job completed: live=%d", f.slots.live)
	}
}

func TestCloseAllDrainsInflightRecv(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ring, err := uring.NewRing(64)
	if err != nil {
		t.Fatal(err)
	}
	defer ring.QueueExit()
	rx, err := uring.NewRXArena(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := uring.NewTXArena(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[1]) })

	f := &fastLoop{
		server: &Server{MaxOutputBytes: 1 << 20},
		ring:   ring,
		rx:     rx,
		tx:     tx,
		start:  time.Now(),
	}
	_, c := f.slots.alloc(fds[0])
	f.submitRecv(c)
	if !c.has(flagRecv) {
		t.Fatal("receive was not submitted")
	}
	start := time.Now()
	f.closeAll()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("closeAll took %v", elapsed)
	}
	if f.slots.live != 0 {
		t.Fatalf("slot still live after shutdown: %d", f.slots.live)
	}
	if f.rx != nil || f.tx != nil {
		t.Fatalf("arenas not closed (ringBroken=%v)", f.ringBroken)
	}
}

func TestCloseAllWithLiveIdleConnections(t *testing.T) {
	rx, _ := uring.NewRXArena(64, 2)
	tx, _ := uring.NewTXArena(128, 64)
	f := &fastLoop{server: &Server{MaxOutputBytes: 1 << 20}, rx: rx, tx: tx, start: time.Now()}
	f.slots.alloc(-1)
	f.slots.alloc(-1)
	f.closeAll()
	if f.slots.live != 0 {
		t.Fatalf("live=%d", f.slots.live)
	}
}
