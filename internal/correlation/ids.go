// Package correlation owns the deterministic identifiers shared by direct
// Agent Observability Generations and the OTLP spans that represent them.
package correlation

import (
	"crypto/sha256"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// TraceID identifies the thread-level Tempo trace containing t's response span.
func TraceID(t *turn.Turn) trace.TraceID {
	key := t.ThreadID
	if key == "" {
		key = t.RequestID
	}
	if key == "" {
		key = t.ResponseID
	}
	if key == "" {
		key = "ts:" + strconv.FormatInt(t.FirstTS.UnixNano(), 10)
	}
	return HashTraceID("thread", key)
}

// ResponseSpanID identifies the response span representing t's Generation.
func ResponseSpanID(t *turn.Turn) trace.SpanID {
	return HashSpanID("response", ResponseKey(t))
}

// ResponseKey is the stable source identity shared by a Generation and its span.
func ResponseKey(t *turn.Turn) string {
	key := t.ResponseID
	if key == "" {
		key = t.RequestID
	}
	if key == "" {
		key = t.LogicalTurnID
	}
	if key == "" {
		key = t.TurnID
	}
	return key
}

// HashTraceID deterministically derives a valid trace ID from identity parts.
func HashTraceID(parts ...string) trace.TraceID {
	sum := sha256.Sum256([]byte(joinParts(parts)))
	var id trace.TraceID
	copy(id[:], sum[:16])
	if !id.IsValid() {
		id[len(id)-1] ^= 0x01
	}
	return id
}

// HashSpanID deterministically derives a valid span ID from identity parts.
func HashSpanID(parts ...string) trace.SpanID {
	sum := sha256.Sum256([]byte(joinParts(parts)))
	var id trace.SpanID
	copy(id[:], sum[16:24])
	if !id.IsValid() {
		id[len(id)-1] ^= 0x01
	}
	return id
}

func joinParts(parts []string) string {
	var b strings.Builder
	for i, part := range parts {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(part)
	}
	return b.String()
}
