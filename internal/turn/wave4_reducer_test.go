package turn

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/frame"
)

func TestWave4ErrorStatusShapes(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  int
		type_ string
	}{
		{
			name:  "status code quota error wins over status",
			raw:   `{"type":"error","status":400,"status_code":429,"error":{"type":"usage_limit_reached"}}`,
			want:  429,
			type_: "usage_limit_reached",
		},
		{
			name:  "status rejection error",
			raw:   `{"type":"error","status":400,"error":{"type":"invalid_request_error"}}`,
			want:  400,
			type_: "invalid_request_error",
		},
		{
			name:  "neither status key",
			raw:   `{"type":"error","error":{"type":"service_unavailable_error"}}`,
			want:  0,
			type_: "service_unavailable_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := &Turn{}
			New().applyError(got, frame.Event{Raw: []byte(tt.raw)})
			if got.UpstreamStatusCode != tt.want {
				t.Errorf("UpstreamStatusCode = %d, want %d", got.UpstreamStatusCode, tt.want)
			}
			if got.Status != StatusError || got.ErrorType != tt.type_ {
				t.Errorf("status/error type = %q/%q, want %q/%q", got.Status, got.ErrorType, StatusError, tt.type_)
			}
		})
	}
}

func TestWave4QuotaFailureAndAdditionalLimits(t *testing.T) {
	got := &Turn{}
	New().applyError(got, frame.Event{Raw: []byte(`{
		"type":"error","status_code":429,
		"error":{"type":"usage_limit_reached","plan_type":"synthetic-plan","resets_at":1234,"resets_in_seconds":0},
		"headers":{
			"X-Codex-Active-Limit":"premium",
			"X-Codex-Credits-Has-Credits":"True",
			"X-Codex-Credits-Unlimited":"invalid",
			"X-Codex-Credits-Balance":"0",
			"X-Codex-Primary-Used-Percent":"10",
			"X-Codex-Primary-Window-Minutes":"60",
			"X-Codex-Primary-Reset-At":"1000",
			"X-Codex-Primary-Reset-After-Seconds":"0",
			"X-Codex-Primary-Over-Secondary-Limit-Percent":"20",
			"X-Codex-Bengalfox-Limit-Name":"synthetic-limit",
			"X-Codex-Bengalfox-Secondary-Used-Percent":"30",
			"X-Codex-Bengalfox-Secondary-Window-Minutes":"120",
			"X-Codex-Bengalfox-Secondary-Reset-At":"2000",
			"X-Codex-Bengalfox-Secondary-Reset-After-Seconds":"40",
			"X-Base-Model-Inference-Limit-Name":"secondary-limit",
			"X-Base-Model-Inference-Primary-Used-Percent":"50",
			"X-Future-Limit-Primary-Used-Percent":"60"
		}
	}`)})

	if got.PlanType != "synthetic-plan" || got.QuotaFailureActiveLimit != "premium" ||
		got.QuotaFailureResetsAt == nil || *got.QuotaFailureResetsAt != 1234 ||
		got.QuotaFailureResetsInSeconds == nil || *got.QuotaFailureResetsInSeconds != 0 {
		t.Fatalf("quota error body = %#v", got)
	}
	if got.QuotaFailureCreditsHas == nil || !*got.QuotaFailureCreditsHas ||
		got.QuotaFailureCreditsUnlimited != nil || got.QuotaFailureCreditsBalance != "0" {
		t.Fatalf("quota credit fields = has:%v unlimited:%v balance:%q", got.QuotaFailureCreditsHas, got.QuotaFailureCreditsUnlimited, got.QuotaFailureCreditsBalance)
	}
	limits := quotaLimitsByFamily(got.QuotaFailureLimits)
	for _, family := range []string{"codex", "bengalfox", "base_model_inference", "future_limit"} {
		if limits[family] == nil {
			t.Errorf("missing %s quota family in %#v", family, got.QuotaFailureLimits)
		}
	}
	if got := limits["bengalfox"]; got.LimitName != "synthetic-limit" || len(got.Windows) != 1 ||
		got.Windows[0].Window != "secondary" || got.Windows[0].ResetAfterSeconds == nil || *got.Windows[0].ResetAfterSeconds != 40 {
		t.Errorf("bengalfox quota = %#v", got)
	}
	if got := limits["codex"]; got.PrimaryOverSecondaryLimitPercent == nil ||
		*got.PrimaryOverSecondaryLimitPercent != 20 || got.Windows[0].ResetAfterSeconds == nil ||
		*got.Windows[0].ResetAfterSeconds != 0 {
		t.Errorf("codex quota = %#v", got)
	}

	r := New()
	r.applyRateLimits(got, frame.Event{Raw: []byte(`{
		"additional_rate_limits":{"synthetic-model":{
			"allowed":false,"limit_reached":true,
			"primary":{"used_percent":0,"window_minutes":30,"reset_after_seconds":0,"reset_at":3000},
			"secondary":null
		}}
	}`)})
	if allowed, ok := got.ExtraRateLimitAllowed["synthetic-model"]; !ok || allowed {
		t.Errorf("additional allowed = %v, present=%v; want present false", allowed, ok)
	}
	if reached, ok := got.ExtraRateLimitReached["synthetic-model"]; !ok || !reached {
		t.Errorf("additional reached = %v, present=%v; want present true", reached, ok)
	}
	if windows := got.ExtraRateLimits["synthetic-model"]; len(windows) != 1 || windows[0].ResetAt != 3000 {
		t.Errorf("additional windows = %#v, want reset_at 3000", windows)
	}
}

func TestWave4MetadataReduction(t *testing.T) {
	metadata := `{"context_window_id":"context-synthetic","workspaces":{"workspace-beta":{"role":"viewer"},"workspace-alpha":{"role":"editor"}},"compaction":{"trigger":"manual","reason":"user_requested","implementation":"responses_compaction_v2","phase":"standalone_turn","strategy":"memento"}}`
	kinds := []string{
		"model.base_instructions", "model_switch.instructions", "memories.instructions", "host_skills.instructions",
		"permissions.instructions", "collaboration_mode.instructions", "apps.instructions", "plugins.usage_instructions",
		"plugins.recommendations", "multi_agent.usage_hint", "multi_agent.mode_instructions", "multi_agent.role_instructions",
		"agents_md.instructions", "environments.environment_context", "hooks.additional_context", "user.text",
		"generic.turn_aborted", "unknown",
	}
	input := fmt.Sprintf(`[{"type":"message","internal_chat_message_metadata_passthrough":{"turn_id":"passthrough-synthetic","create_time":0,"content_item_kinds":%s}}]`, mustJSON(t, kinds))
	create := fmt.Sprintf(`{"type":"response.create","model":"synthetic-model","client_metadata":{"x-codex-turn-metadata":%s},"input":%s}`, strconv.Quote(metadata), input)
	r := New()
	_, err := r.Add(&frame.Record{
		RequestID: "request-synthetic",
		Headers: frame.Headers{
			"x-codex-routing-hint": "model=synthetic-model",
			frame.HdrTurnMetadata:  `{"compaction":{"trigger":"header-only"}}`,
		},
		Payload: frame.Payload{Text: create},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Add(&frame.Record{
		RequestID: "request-synthetic",
		Payload:   frame.Payload{Text: `{"type":"codex.response.metadata","headers":{"X-Models-ETag":"synthetic-etag","X-Codex-Turn-State":"state-synthetic"}}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.open["request-synthetic"]
	if got == nil {
		t.Fatal("open turn was not retained")
	}
	if got.RoutingHintAgreement != "agree" || got.PassthroughTurnID != "passthrough-synthetic" ||
		got.PassthroughCreateTime != 0 || got.ModelsETag != "synthetic-etag" || got.TurnState != "state-synthetic" ||
		got.ContextWindowID != "context-synthetic" {
		t.Errorf("metadata reduction = %#v", got)
	}
	if got.Workspaces != `{"workspace-alpha":{"role":"editor"},"workspace-beta":{"role":"viewer"}}` {
		t.Errorf("stable workspaces = %q", got.Workspaces)
	}
	if got.CompactionTrigger != "manual" || got.CompactionReason != "user_requested" ||
		got.CompactionImplementation != "responses_compaction_v2" || got.CompactionPhase != "standalone_turn" ||
		got.CompactionStrategy != "memento" {
		t.Errorf("compaction reduction = %#v", got)
	}
	if len(got.ContentItemKinds) != len(kinds) {
		t.Fatalf("content item kinds = %#v", got.ContentItemKinds)
	}
	for _, kind := range kinds {
		if got.ContentItemKinds[kind] != 1 {
			t.Errorf("content item kind %q = %d, want 1", kind, got.ContentItemKinds[kind])
		}
	}
}

func TestWave4RoutingHintAgreement(t *testing.T) {
	for _, tt := range []struct {
		hint, model, want string
	}{
		{"model=synthetic", "other", "disagree"},
		{"", "synthetic", "absent"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			got := &Turn{Model: tt.model}
			applyRoutingHint(got, tt.hint)
			if got.RoutingHintAgreement != tt.want {
				t.Errorf("agreement = %q, want %q", got.RoutingHintAgreement, tt.want)
			}
		})
	}
}

func quotaLimitsByFamily(limits []QuotaFailureLimit) map[string]*QuotaFailureLimit {
	got := make(map[string]*QuotaFailureLimit, len(limits))
	for i := range limits {
		got[limits[i].Family] = &limits[i]
	}
	return got
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWave4QuotaHeadersEmptyStringIsAbsentButZeroIsMeasured(t *testing.T) {
	got := &Turn{}
	New().applyError(got, frame.Event{Raw: []byte(`{
		"type":"error",
		"status_code":429,
		"error":{"type":"usage_limit_reached"},
		"headers":{
			"X-Codex-Secondary-Reset-At":"",
			"X-Codex-Secondary-Reset-After-Seconds":"0"
		}
	}`)})

	if len(got.QuotaFailureLimits) != 1 || len(got.QuotaFailureLimits[0].Windows) != 1 {
		t.Fatalf("quota failure limits = %#v, want Codex secondary window", got.QuotaFailureLimits)
	}
	window := got.QuotaFailureLimits[0].Windows[0]
	if window.ResetAt != nil {
		t.Errorf("empty ResetAt = %v, want nil", *window.ResetAt)
	}
	if window.ResetAfterSeconds == nil || *window.ResetAfterSeconds != 0 {
		t.Errorf("zero ResetAfterSeconds = %v, want non-nil zero", window.ResetAfterSeconds)
	}
}
