# Build the CLI tools into bin/ so they can be run directly. `go run ./cmd/<tool>`
# works too and needs no build step, but recompiles on every invocation - which is
# noticeable on tools you run interactively.
#
# bin/ is gitignored.

TOOLS := codexlb2otel clbsync clbfind clbsum clbprobe clbprofile clbstat
BINS  := $(addprefix bin/,$(TOOLS))

CORPUS ?= corpus/processed

# Go 1.27's new encoding/json implementation makes the multi-gigabyte corpus
# scans exceed the package timeout. Keep the established v1 implementation until
# the archive decoder is migrated and measured against json/v2 explicitly.
GOEXPERIMENT ?= nojsonv2
export GOEXPERIMENT

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

# Run the service against a config file. Live push - see config.example.yaml.
.PHONY: run
run: bin/codexlb2otel
	./bin/codexlb2otel -config $(CONFIG)

CONFIG ?= config.yaml

.PHONY: clean
clean:
	rm -rf bin

# Pull anything new off the codex-lb host, then offer to check it for drift.
.PHONY: sync
sync: bin/clbsync
	./bin/clbsync

# Drift check against the committed baseline. Full by default. Exits 1 on anything new.
.PHONY: probe
probe: bin/clbprobe
	./bin/clbprobe $(CORPUS)

# Sampled drift check. Faster, and cannot prove a shape is absent.
.PHONY: probe-sampled
probe-sampled: bin/clbprobe
	./bin/clbprobe -sampled $(CORPUS)

# Accept the current shape as the baseline. Deliberate act - always from a FULL
# scan, or the baseline silently omits every rare shape the sample missed.
.PHONY: baseline
baseline: bin/clbprobe
	./bin/clbprobe -update corpus.sig.json $(CORPUS)

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
