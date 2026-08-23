package agento11y

import (
	"context"
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/config"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestEmitKeepsIdentifiedTurnsWithoutAModel(t *testing.T) {
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
	if len(s.buf) != 3 || s.buf[0].ID != "transport-only" || s.buf[1].ID != "blank-model" || s.buf[2].ID != "generation" {
		t.Fatalf("buffer = %+v, want every identified generation including unknown-model records", s.buf)
	}
	for _, g := range s.buf[:2] {
		if g.Model == nil || g.Model.Name != "unknown" || g.Model.Provider != "codex" {
			t.Errorf("model-less generation = %+v, want codex/unknown", g.Model)
		}
	}
}
