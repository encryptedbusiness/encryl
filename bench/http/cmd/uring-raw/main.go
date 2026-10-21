//go:build linux

// Command uring-raw runs the TFB JSON/Plaintext shape on the direct
// httpuring HTTP/1.1 loop. It does not use net/http or fasthttp.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime/pprof"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/encryptedbusiness/encryl/bench/http/internal/payload"
	"github.com/encryptedbusiness/encryl/httpuring"
)

var (
	jsonType  = []byte("application/json")
	plainType = []byte("text/plain")
	plainBody = []byte(payload.Plaintext)
)

var (
	serverHist  [20000]atomic.Uint64
	serverTotal atomic.Uint64
)

func recordLatency(d time.Duration) {
	us := d.Microseconds()
	i := int(us / 100)
	if i >= len(serverHist) {
		i = len(serverHist) - 1
	}
	serverHist[i].Add(1)
	serverTotal.Add(1)
}

func printLatency() {
	total := serverTotal.Load()
	if total == 0 {
		return
	}
	var cum uint64
	var p50, p99, p999, p9999, max int64
	for i := range serverHist {
		cum += serverHist[i].Load()
		if p50 == 0 && cum*100 >= total*50 {
			p50 = int64(i+1) * 100
		}
		if p99 == 0 && cum*100 >= total*99 {
			p99 = int64(i+1) * 100
		}
		if p999 == 0 && cum*1000 >= total*999 {
			p999 = int64(i+1) * 100
		}
		if p9999 == 0 && cum*10000 >= total*9999 {
			p9999 = int64(i+1) * 100
		}
		if cum == total {
			max = int64(i+1) * 100
			break
		}
	}
	log.Printf("server latency n=%d p50=%dus p99=%dus p99.9=%dus p99.99=%dus max~%dus", total, p50, p99, p999, p9999, max)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18103", "listen address")
	loops := flag.Int("loops", 1, "SO_REUSEPORT ring loops")
	serverStats := flag.Bool("server-stats", false, "record ring-loop request latency")
	flag.Parse()

	mux := &httpuring.Mux{}
	mux.HandleFunc("/json", func(req *httpuring.Request, res *httpuring.Response) {
		res.ContentType = jsonType
		res.Body = payload.AppendMessage(res.Body, payload.Message{Message: "Hello, World!"})
	})
	mux.HandleFunc("/plaintext", func(req *httpuring.Request, res *httpuring.Response) {
		res.ContentType = plainType
		res.Body = plainBody
	})

	srv := &httpuring.Server{Addr: *addr, Handler: mux, ServerHeader: "encryl", Loops: *loops}
	if *serverStats {
		srv.OnRequest = recordLatency
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR2)
	go func() {
		for range sig {
			f, err := os.Create("/tmp/uring-raw-heap.prof")
			if err == nil {
				_ = pprof.Lookup("heap").WriteTo(f, 0)
				_ = f.Close()
			}
		}
	}()
	statsSig := make(chan os.Signal, 1)
	signal.Notify(statsSig, syscall.SIGUSR1)
	go func() {
		for range statsSig {
			printLatency()
		}
	}()
	log.Printf("uring-raw benchmark listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
