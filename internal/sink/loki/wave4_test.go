package loki

import (
	"testing"
	"time"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// TestBuildLines_Wave4IdentityFieldsUseStructuredMetadata proves the Loki path
// carries the frozen Wave 4 identity fields on the lines that contain a turn.
// The same metadata is built once and reused for content records by buildLines;
// checking every produced line therefore also protects that fan-out contract.
func TestBuildLines_Wave4IdentityFieldsUseStructuredMetadata(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	turnWithIdentity := &turn.Turn{
		RequestID:             "request-wave4",
		ResponseID:            "response-wave4",
		ThreadID:              "thread-wave4",
		Family:                "websocket",
		Status:                "completed",
		FirstTS:               now,
		LastTS:                now.Add(time.Second),
		ServerCompletedAt:     now.Add(time.Second),
		ConversationID:        "conversation-wave4",
		ProxySessionID:        "proxy-session-wave4",
		Workspaces:            `{"/workspace/project":["repo"]}`,
		TurnState:             "encrypted-turn-state-wave4",
		ModelsETag:            `W/"0123456789abcdef0123456789abcdef"`,
		ContextWindowID:       "context-window-wave4",
		PassthroughTurnID:     "turn-passthrough-wave4",
		PassthroughCreateTime: 123.5,
		SafetyID:              "safety-wave4",
		Prompts: []turn.Prompt{{
			Role: "user", Text: "synthetic prompt", Ordinal: 1,
		}},
		Messages: []turn.Message{{
			Text: "synthetic response", Ordinal: 2,
		}},
	}

	want := map[string]string{
		attr.Workspaces:            turnWithIdentity.Workspaces,
		attr.TurnState:             turnWithIdentity.TurnState,
		attr.ConversationID:        turnWithIdentity.ConversationID,
		attr.ProxySessionID:        turnWithIdentity.ProxySessionID,
		attr.ModelsETag:            turnWithIdentity.ModelsETag,
		attr.ContextWindowID:       turnWithIdentity.ContextWindowID,
		attr.PassthroughTurnID:     turnWithIdentity.PassthroughTurnID,
		attr.PassthroughCreateTime: "123.5",
		attr.SafetyID:              turnWithIdentity.SafetyID,
	}

	guard := attr.NewGuard()
	lines := buildLines(turnWithIdentity, guard, "codexlb2otel", attr.DefaultLabels, 192<<10, nil, newFakeRejecter())
	if len(lines) < 3 {
		t.Fatalf("buildLines produced %d lines, want turn plus content lines", len(lines))
	}

	for _, line := range lines {
		metadata := kvMap(line.metadata)
		labels := kvMap(line.labels)
		for key, value := range want {
			lokiKey := attr.LokiKey(key)
			if got := metadata[lokiKey]; got != value {
				t.Errorf("%s metadata[%s] = %q, want %q", line.recordType, lokiKey, got, value)
			}
			if got, ok := labels[lokiKey]; ok {
				t.Errorf("identity field %s became a Loki stream label with value %q", key, got)
			}
		}
	}

	// A misconfigured caller cannot promote an identity field either: Labels
	// filters by class even before New validates the configured set. Keep this
	// separate from the metadata assertion above because a promoted key is
	// intentionally omitted from Metadata.
	requestedLabels := append(append([]string(nil), attr.DefaultLabels...), identityKeys(want)...)
	labels := kvMap(guard.Labels(turnWithIdentity, "codexlb2otel", attr.RecordTurn, requestedLabels))
	for key := range want {
		if value, ok := labels[attr.LokiKey(key)]; ok {
			t.Errorf("identity field %s became a Loki stream label with value %q", key, value)
		}
	}
}

func identityKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
