SHELL := /bin/bash

GO ?= go
LIMA_NAME ?= encryl-dev
VENDORED := uring/giouring
LINUX_RUN := ./scripts/linux-run.sh

PACKAGES := $(shell $(GO) list ./... | grep -v $(VENDORED))
RACE_PACKAGES := $(shell $(GO) list ./... | grep -v $(VENDORED))

.PHONY: all fmt fmt-check vet lint test test-race test-adapters \
        test-race-adapters checkptr cross fuzz examples qualify-messages qualify-rate \
        uring-test preflight docs-check bench-baseline bench-rooms soak \
        autobahn test-uring linux-shell limactl-up vuln clean \
        gate-a gate-b gate-c gate-e

all: fmt vet test

fmt:
	$(GO) fmt ./...

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet $(PACKAGES)

# The vendored liburing port contains intentional unsafe.Pointer arithmetic;
# staticcheck cannot reason about it. It is excluded from lint and covered by
# the kernel job instead.
lint:
	@command -v golangci-lint >/dev/null || (echo "golangci-lint not installed"; exit 1)
	golangci-lint run --timeout 5m

test:
	$(GO) test $(PACKAGES)

test-adapters:
	cd adapters && $(GO) test ./...

bench-http:
	cd bench/http && $(GO) build ./cmd/uring ./cmd/uring-raw ./cmd/uring-chi ./cmd/fiber ./cmd/fasthttp-uring

examples:
	$(GO) build -o /dev/null ./examples/echo ./examples/chat ./examples/http ./examples/nethttp ./examples/mux ./examples/uringhttp ./examples/uringhttp_echo
	cd examples/fiber && $(GO) build -o /dev/null .
	cd examples/uringhttp_chi && $(GO) build -o /dev/null .
	cd examples/uringhttp_websocket && $(GO) build -o /dev/null .

qualify-messages:
	./scripts/qualify-messages.sh

qualify-rate:
	./scripts/qualify-rate.sh
	./scripts/qualify-messages.sh

test-race-adapters:
	cd adapters && $(GO) test -race ./...

test-race:
	$(GO) test -race $(RACE_PACKAGES)

checkptr:
	$(GO) test -gcflags=all=-d=checkptr=2 $(RACE_PACKAGES)
	cd adapters && $(GO) test -gcflags=all=-d=checkptr=2 ./...

cross:
	GOOS=linux GOARCH=amd64 $(GO) build ./...
	GOOS=linux GOARCH=arm64 $(GO) build ./...
	GOOS=darwin GOARCH=arm64 $(GO) build ./...

fuzz:
	$(GO) test -run=NONE -fuzz=FuzzFrame -fuzztime=60s ./ws
	$(GO) test -run=NONE -fuzz=FuzzUpgrade -fuzztime=60s ./httpx
	$(GO) test -run=NONE -fuzz=FuzzEnvelope -fuzztime=60s ./event
	$(GO) test -run=NONE -fuzz=FuzzDecodeCommands -fuzztime=60s ./protocol

# --- Linux-only targets -----------------------------------------------------
# These require a Linux 6.6+ target. By default they run inside the Lima VM
# described in deploy/lima.yaml. Set ENCRYL_LINUX_HOST=user@host to use SSH.

limactl-up:
	limactl start --name=$(LIMA_NAME) deploy/lima.yaml

linux-shell:
	$(LINUX_RUN) bash

uring-test:
	$(LINUX_RUN) make _uring-test

_uring-test:
	$(GO) test -count=1 ./uring ./uring/giouring ./reactor

test-uring:
	$(LINUX_RUN) make _test-uring

_test-uring:
	$(GO) test -count=1 -v ./reactor ./timerwheel ./ws ./httpx ./event \
		./room ./connslab ./uring ./netio

autobahn:
	$(LINUX_RUN) ./scripts/autobahn.sh

# M2 gates. Host and sysctl details are in docs/benchmarks/2026-10-23-m2-vertical-slices.md.
gate-a:
	$(LINUX_RUN) env CONNS=$(CONNS) SHARDS=$(SHARDS) RATE=$(RATE) HOLD=$(HOLD) \
		SRC_IPS=$(SRC_IPS) RAMP_TIMEOUT=$(RAMP_TIMEOUT) MAX_PER_CONN=$(MAX_PER_CONN) \
		./scripts/memory.sh

gate-b:
	$(LINUX_RUN) env CONNS=$(CONNS) SHARDS=$(SHARDS) SRC_IPS=$(SRC_IPS) \
		RATE=$(RATE) MONITOR=$(MONITOR) DRAIN=$(DRAIN) MSG_RATE=$(MSG_RATE) \
		./scripts/gate-b.sh

gate-c:
	$(LINUX_RUN) env CONNS=$(CONNS) RATE=$(RATE) DURATION=$(DURATION) \
		./scripts/gate-c.sh

# Runs on any platform; the slice benchmarks are portable.
gate-e:
	$(GO) test -run=NONE -bench=. -benchmem -benchtime=1s ./bench/...

bench-rooms:
	CONNS=$(CONNS) $(LINUX_RUN) make _bench-rooms

_bench-rooms:
	$(GO) test -run=NONE -bench=BenchmarkSnapshot1000 -benchmem ./bench/room

soak:
	CONNS=$(CONNS) DURATION=$(DURATION) $(LINUX_RUN) make _soak

_soak:
	./scripts/soak.sh

preflight:
	$(LINUX_RUN) make _preflight

_preflight:
	@echo "== host =="
	@uname -a
	@echo "== limits =="
	@ulimit -n
	@sysctl -n net.core.somaxconn
	@echo "== io_uring =="
	@$(GO) test -count=1 ./uring

bench-baseline:
	CONNS=$(CONNS) DURATION=$(DURATION) $(LINUX_RUN) make _bench-baseline

_bench-baseline:
	./scripts/bench-baseline.sh

docs-check:
	./scripts/docs-check.sh

vuln:
	@command -v govulncheck >/dev/null || (echo "govulncheck not installed: go install golang.org/x/vuln/cmd/govulncheck@latest"; exit 1)
	govulncheck ./...

clean:
	rm -rf bin dist
