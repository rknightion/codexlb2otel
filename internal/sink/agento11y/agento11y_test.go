package agento11y

import (
	"context"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestEmitSkipsTurnsWithoutAModel(t *testing.T) {
	s := &Sink{
		cfg:   config.AgentO11y{BatchSize: 100, BatchWait: time.Hour},
		guard: attr.NewGuard(),
	}
	turns := []*turn.Turn{
		{RequestID: "transport-only", Status: turn.StatusTransport},
		{RequestID: "blank-model", Model: " \t"},
		{Model: "gpt-5.6-sol"},
		{RequestID: "generation", Model: "gpt-5.6-sol"},
	}
	if err := s.Emit(context.Background(), turns); err != nil {
		t.Fatal(err)
	}
	if len(s.buf) != 1 || s.buf[0].ID != "generation" {
		t.Fatalf("buffer = %+v, want only the model-backed generation", s.buf)
	}
}
