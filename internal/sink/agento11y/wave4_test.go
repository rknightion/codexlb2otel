package agento11y

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/grafana/agento11y/go/proto/agento11y/wire"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// TestBuildGeneration_Wave4StructuredMetadata reaches Agent Observability's
// tags map through the real protojson-shaped request. Tags use the same guarded
// span-attribute contract as the OTLP trace sink, so this protects both sinks
// from independently losing a newly registered structured field.
func TestBuildGeneration_Wave4StructuredMetadata(t *testing.T) {
	turnWithMetadata := &turn.Turn{
		RequestID:                "request-wave4",
		ResponseID:               "response-wave4",
		ThreadID:                 "thread-wave4",
		Model:                    "gpt-5.6-sol",
		Status:                   "completed",
		FirstTS:                  time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC),
		LastTS:                   time.Date(2026, 9, 12, 12, 0, 1, 0, time.UTC),
		CompactionTrigger:        "manual",
		CompactionReason:         "user_requested",
		CompactionImplementation: "responses_compaction_v2",
		CompactionPhase:          "standalone_turn",
		CompactionStrategy:       "memento",
		RoutingHintAgreement:     "agree",
		ServiceTierOutcome:       "granted",
		StickyKind:               "prompt_cache",
		StickyKeySource:          "thread_header",
		ProxyRouteMode:           "direct",
		Workspaces:               `{"/workspace/project":["repo"]}`,
		TurnState:                "encrypted-turn-state-wave4",
		ConversationID:           "conversation-wave4",
		ProxySessionID:           "proxy-session-wave4",
		ModelsETag:               `W/"0123456789abcdef0123456789abcdef"`,
		ContextWindowID:          "context-window-wave4",
		PassthroughTurnID:        "turn-passthrough-wave4",
		PassthroughCreateTime:    123.5,
		SafetyID:                 "safety-wave4",
	}

	want := map[string]string{
		attr.CompactionTrigger:        turnWithMetadata.CompactionTrigger,
		attr.CompactionReason:         turnWithMetadata.CompactionReason,
		attr.CompactionImplementation: turnWithMetadata.CompactionImplementation,
		attr.CompactionPhase:          turnWithMetadata.CompactionPhase,
		attr.CompactionStrategy:       turnWithMetadata.CompactionStrategy,
		attr.RoutingHintAgreement:     turnWithMetadata.RoutingHintAgreement,
		attr.ServiceTierOutcome:       turnWithMetadata.ServiceTierOutcome,
		attr.StickyKind:               turnWithMetadata.StickyKind,
		attr.StickyKeySource:          turnWithMetadata.StickyKeySource,
		attr.ProxyRouteMode:           turnWithMetadata.ProxyRouteMode,
		attr.Workspaces:               turnWithMetadata.Workspaces,
		attr.TurnState:                turnWithMetadata.TurnState,
		attr.ConversationID:           turnWithMetadata.ConversationID,
		attr.ProxySessionID:           turnWithMetadata.ProxySessionID,
		attr.ModelsETag:               turnWithMetadata.ModelsETag,
		attr.ContextWindowID:          turnWithMetadata.ContextWindowID,
		attr.PassthroughTurnID:        turnWithMetadata.PassthroughTurnID,
		attr.PassthroughCreateTime:    "123.5",
		attr.SafetyID:                 turnWithMetadata.SafetyID,
	}

	generation := buildGeneration(turnWithMetadata, attr.NewGuard())
	data, err := json.Marshal(wireExportGenerationsRequest{Generations: []wireGeneration{generation}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	request, err := wire.UnmarshalExportGenerationsJSON(data)
	if err != nil {
		t.Fatalf("wire.UnmarshalExportGenerationsJSON rejected sink output: %v", err)
	}
	if len(request.Generations) != 1 {
		t.Fatalf("decoded %d generations, want 1", len(request.Generations))
	}

	got := request.Generations[0].GetTags()
	for key, value := range want {
		if got[key] != value {
			t.Errorf("generation tag[%s] = %q, want %q", key, got[key], value)
		}
	}
}
