# syntax=docker/dockerfile:1.7
#
# Multi-stage build for codexlb2otel (issue #10). Packaging only - see the issue's
# 2026-08-07 binding decision: this image is NOT deployed by this change, and
# nothing in this repo runs `docker compose up` against a real host.
#
# GO_VERSION intentionally floats rather than hardcoding a version: docker.yml (CI)
# extracts it straight from go.mod's `go` directive with
# `sed -n 's/^go //p' go.mod` and passes it as --build-arg, so the toolchain that
# builds the release image can never silently drift from what the module actually
# declares. The default below exists ONLY so a bare `docker build .` works
# unassisted for local iteration - it is not consulted by CI (--build-arg always
# wins) and is not itself the source of truth, so it is allowed to go stale between
# go.mod bumps without breaking anything that matters.
ARG GO_VERSION=1.26.5

FROM golang:${GO_VERSION}-alpine AS builder

# Pure Go all the way down (net/http, archive/gzip, gopkg.in/yaml.v3, the OTel SDK,
# agento11y's client) - CGO_ENABLED=0 costs nothing here and is what makes the
# final stage's from-scratch-style base possible at all.
ENV CGO_ENABLED=0 GOOS=linux

WORKDIR /src

# go.mod/go.sum copied and downloaded separately from the source tree so editing a
# .go file never busts the module-download cache layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Only the two directories the build actually needs - not `COPY . .` - so editing
# corpus/, docs/, or a workflow file never busts this layer either, and so nothing
# under corpus/ (see .dockerignore: real captured archive content can exist there
# on disk despite being gitignored) can ever reach an image layer through this
# stage even if .dockerignore were somehow bypassed.
COPY cmd ./cmd
COPY internal ./internal

# -trimpath drops local build-machine filesystem paths from the binary (so a panic
# stack trace never leaks /src, and so the build is reproducible across machines).
# -ldflags="-s -w" strips the symbol table and DWARF debug info - the smallest,
# always-correct form of "strip the binary" available. No -X version injection:
# checked cmd/codexlb2otel and internal/ for a Version/BuildInfo var to stamp and
# there isn't one (grep for `var Version\|var version\|const Version` turns up
# nothing) - the release tag on the image itself is the version record instead of
# inventing a symbol nothing reads.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/codexlb2otel ./cmd/codexlb2otel

# -----------------------------------------------------------------------------
# distroless/static: no shell, no package manager, no libc even (the binary above
# is fully static) - and the `nonroot` variant already runs as uid/gid 65532 by
# default. Pinned by digest, unlike the builder stage above: this base has no
# relationship to go.mod's Go version, so nothing here should ever need to float.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35

COPY --from=builder /out/codexlb2otel /codexlb2otel

# Restated explicitly rather than left implicit in the base image's default: a
# future base-image swap that changes the default user then fails this build
# loudly (unknown user) instead of silently regaining root.
USER 65532:65532

# archive.dir, archive.checkpoint, and the config file itself all arrive as
# read-only/writable mounts at run time (see docker-compose.yml) - nothing under
# those paths is created or assumed to pre-exist in the image.

# The probe is the service binary itself (-healthcheck), not an HTTP client copied
# into the image.
#
# The alternative was lifting busybox's wget applet into this layer, and that
# quietly defeats the whole point of distroless: busybox is ONE multi-call ELF that
# dispatches on argv[0], so anything able to exec it can ask for the sh applet
# whatever the file on disk is named. Renaming a shell is not removing one. Using
# the binary already here costs nothing and leaves genuinely no shell to reach.
#
# It also fixes a second problem: -healthcheck loads the same config file the server
# does, so it probes whatever health.listen is actually deployed. A hardcoded
# host:port here would silently probe the wrong address the moment a deployment
# overrides it - and a healthcheck that cannot reach the service reports unhealthy,
# so the container would be killed and restarted forever over a config mismatch.
#
# 503 while the watcher has not started or is draining, 200 once ready
# (internal/health's SetReady); Probe treats any non-200 as failure.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/codexlb2otel", "-healthcheck"]

EXPOSE 9464

ENTRYPOINT ["/codexlb2otel"]
CMD ["-config", "/etc/codexlb2otel/config.yaml"]
