# Build the CLI tools into bin/ so they can be run directly. `go run ./cmd/<tool>`
# works too and needs no build step, but recompiles on every invocation - which is
# noticeable on tools you run interactively.
#
# bin/ is gitignored.

TOOLS := clbsync clbfind clbprobe clbprofile clbstat
BINS  := $(addprefix bin/,$(TOOLS))

CORPUS ?= corpus/processed

# Every Go file, not just the tool's own directory. Depending on `cmd/<tool>` looks
# right and silently never rebuilds: a directory's mtime does not change when a file
# inside it is edited, so an edited main.go left a stale binary in place. go build
# caches, so the over-broad dependency costs nothing.
GO_FILES := $(shell find . -name '*.go' -not -path './bin/*' 2>/dev/null)

.PHONY: build
build: $(BINS)

bin/%: $(GO_FILES)
	@mkdir -p bin
	go build -o $@ ./cmd/$*

.PHONY: clean
clean:
	rm -rf bin

# Pull anything new off the codex-lb host, then offer to check it for drift.
.PHONY: sync
sync: bin/clbsync
	./bin/clbsync

# Fast drift check against the committed baseline. Exits 1 on anything new.
.PHONY: probe
probe: bin/clbprobe
	./bin/clbprobe $(CORPUS)

# Exhaustive drift check. Required before concluding a shape is absent.
.PHONY: probe-full
probe-full: bin/clbprobe
	./bin/clbprobe -full $(CORPUS)

# Accept the current shape as the baseline. Deliberate act - always from a FULL
# scan, or the baseline silently omits every rare shape the sample missed.
.PHONY: baseline
baseline: bin/clbprobe
	./bin/clbprobe -full -update corpus.sig.json $(CORPUS)

.PHONY: test
test:
	go test ./...

# The corpus tests are the slow ones; this is the fast inner loop.
.PHONY: test-short
test-short:
	CLB_CORPUS=/nonexistent CLB_NO_CORPUS=1 go test ./...

.PHONY: check
check:
	gofmt -l .
	go vet ./...
	go test ./...
