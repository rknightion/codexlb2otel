set shell := ["bash", "-euo", "pipefail", "-c"]

# Go 1.27's json/v2-backed implementation regresses the multi-gigabyte corpus
# scans (see the go.mod rationale). Keep the established v1
# implementation until the archive decoder is migrated and measured against
# json/v2 explicitly. Exported so every recipe below inherits it.
export GOEXPERIMENT := env('GOEXPERIMENT', 'nojsonv2')

# Directory clbprobe/clbsync operate against. Override: `just corpus=other/dir probe`.
corpus := env('CORPUS', 'corpus/processed')
tools := justfile_directory() / ".tools"

# renovate: datasource=github-releases depName=golangci/golangci-lint
golangci_lint_version := "v2.13.2"

# renovate: datasource=go depName=golang.org/x/vuln
govulncheck_version := "v1.7.0"

# show the task surface
default:
    @just --list

# install Go module dependencies into the local module cache
setup:
    go mod download

# format all Go source and this justfile in place
[group('check')]
fmt:
    gofmt -w .
    just --fmt

# verify Go source and this justfile are formatted; never mutates
[group('check')]
fmt-check:
    test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
    just --fmt --check

# run go vet and golangci-lint, including gosec
[group('check')]
[no-exit-message]
lint: _golangci-lint
    go vet ./...
    '{{ tools }}/golangci-lint' run

# run the full Go test suite with the race detector; set filter="Name" for a subset
[group('check')]
[no-exit-message]
test filter="":
    go test -race -timeout 30m -run '{{ filter }}' ./...

# fast inner loop: race-test without the local corpus, exactly as CI does
[group('check')]
[no-exit-message]
test-short:
    CLB_CORPUS=/nonexistent CLB_NO_CORPUS=1 go test -race ./...

# build all seven CLI tools into bin/ (gitignored)
[group('build')]
build:
    mkdir -p bin
    go build -o bin/codexlb2otel ./cmd/codexlb2otel
    go build -o bin/clbsync ./cmd/clbsync
    go build -o bin/clbfind ./cmd/clbfind
    go build -o bin/clbsum ./cmd/clbsum
    go build -o bin/clbprobe ./cmd/clbprobe
    go build -o bin/clbprofile ./cmd/clbprofile
    go build -o bin/clbstat ./cmd/clbstat

# remove output that setup, build, and the pinned tool recipes can reproduce
[group('build')]
clean:
    rm -rf bin dist '{{ tools }}'

# scan the module graph for known vulnerabilities
[group('check')]
[no-exit-message]
vuln: _govulncheck
    '{{ tools }}/govulncheck' ./...

# THE GATE: exactly what CI's build-test job enforces.
[group('check')]
check: fmt-check lint build test-short probe-ci

# CI's superset: snapshot needs cross-compilation and image needs a Docker daemon.
[group('check')]
ci: check snapshot image

# run codexlb2otel against a config file (long-running; pass a path to override)
[group('dev')]
run config="config.yaml":
    mkdir -p bin
    go build -o bin/codexlb2otel ./cmd/codexlb2otel
    ./bin/codexlb2otel -config '{{ config }}'

# pull new archives off the codex-lb host
[group('dev')]
sync:
    mkdir -p bin
    go build -o bin/clbsync ./cmd/clbsync
    ./bin/clbsync

# full drift check against the embedded baseline; override with `just corpus=path probe`
[group('check')]
probe:
    mkdir -p bin
    go build -o bin/clbprobe ./cmd/clbprobe
    ./bin/clbprobe '{{ corpus }}'

# sampled drift check; faster but cannot prove a shape is absent
[group('check')]
probe-sampled:
    mkdir -p bin
    go build -o bin/clbprobe ./cmd/clbprobe
    ./bin/clbprobe -sampled '{{ corpus }}'

# accept the current corpus shape as the embedded baseline (always run from a full scan)
[confirm('This overwrites internal/profile/baseline/corpus.sig.json from a full scan of the local corpus. Continue?')]
[group('gen')]
baseline:
    mkdir -p bin
    go build -o bin/clbprobe ./cmd/clbprobe
    ./bin/clbprobe -update internal/profile/baseline/corpus.sig.json '{{ corpus }}'

# CI's drift probe: scan corpus/ and pass only clean or intentionally absent data.
[group('check')]
[script('bash')]
probe-ci:
    set -euo pipefail
    go build -o /tmp/clbprobe ./cmd/clbprobe
    set +e
    /tmp/clbprobe -fail-on breaking corpus
    status=$?
    set -e
    case "$status" in
      0) echo "clbprobe: clean, no drift against the embedded baseline" ;;
      3) echo "clbprobe: nothing to scan - skipped" ;;
      1) echo "clbprobe: drift at or above 'breaking' against the embedded baseline"; exit 1 ;;
      *) echo "clbprobe: exit $status (see output above)"; exit 1 ;;
    esac

# build the runtime container image; pass a tag to override the default
[group('build')]
image tag="codexlb2otel:dev":
    docker build --build-arg "GO_VERSION=$(awk '/^go / { print $2; exit }' go.mod)" -t '{{ tag }}' .

# cross-compile the release binary without publishing, signing, or producing an SBOM
[group('build')]
snapshot:
    goreleaser release --snapshot --clean --skip=publish,sign,sbom

[private]
_golangci-lint:
    mkdir -p '{{ tools }}'
    GOBIN='{{ tools }}' go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{ golangci_lint_version }}

[private]
_govulncheck:
    mkdir -p '{{ tools }}'
    GOBIN='{{ tools }}' go install golang.org/x/vuln/cmd/govulncheck@{{ govulncheck_version }}
