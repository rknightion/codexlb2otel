package attr

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func keys(kvs []KV) []string {
	out := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		out = append(out, kv.Key)
	}
	return out
}

func find(kvs []KV, key string) (string, bool) {
	for _, kv := range kvs {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return "", false
}

func sampleTurn() *turn.Turn {
	code := 1000
	return &turn.Turn{
		RequestID: "ws_481fa8fa", ResponseID: "resp_052b6a", ThreadID: "019fdb99-a66a",
		SessionID: "019fdb97-294e", TurnID: "019fdb99-a6ac", ParentTurnID: "019fdb97-50c9",
		ParentThreadID: "019fdb97-294e", AccountID: "bd537096-499f-46f4-b1d8-20b1994b9ac5",
		SafetyID: "a-single-human", Model: "gpt-5.6-sol", Status: "completed",
		Effort: "xhigh", Family: "websocket", RequestKind: "turn", ThreadSource: "subagent",
		PlanType: "pro", ServiceTier: "default", Originator: "codex-tui",
		CloseCode: &code, ErrorMessage: "failed for request ws_481fa8fa",
	}
}

func TestAgentNameAvoidsAgentO11ySubagentMarker(t *testing.T) {
	tests := []struct {
		name       string
		originator string
		want       string
	}{
		{name: "tui", originator: "codex-tui", want: "codexlb-codex-tui"},
		{name: "exec", originator: "codex_exec", want: "codexlb-codex_exec"},
		{name: "probe", originator: "codex_cli_rs", want: "codexlb-codex_cli_rs"},
		{name: "missing originator", want: "codexlb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AgentName(&turn.Turn{Originator: tt.originator})
			if got != tt.want {
				t.Errorf("AgentName() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "/") {
				t.Errorf("AgentName() = %q contains Agent Observability's subagent marker", got)
			}
		})
	}
}

// safety_identifier maps 1:1 to a human. #3 puts it in structured metadata and nowhere
// else, and this is the assertion that keeps it there - including out of span
// attributes, because Tempo indexes those for search in a way metadata is not.
func TestSafetyIDReachesStructuredMetadataAndNothingElse(t *testing.T) {
	g := NewGuard()
	tn := sampleTurn()

	for name, kvs := range map[string][]KV{
		"metric attributes": g.MetricAttrs(tn),
		"loki labels":       g.Labels(tn, "codexlb2otel", RecordTurn, append(DefaultLabels, GenAIRequestModel)),
		"span attributes":   g.SpanAttrs(tn),
	} {
		if v, ok := find(kvs, SafetyID); ok {
			t.Errorf("safety_identifier reached %s as %q", name, v)
		}
		for _, kv := range kvs {
			if kv.Value == tn.SafetyID {
				t.Errorf("the safety id leaked into %s under key %q", name, kv.Key)
			}
		}
	}

	if v, ok := find(g.Metadata(tn, DefaultLabels), SafetyID); !ok || v != tn.SafetyID {
		t.Error("safety_identifier is absent from structured metadata, where it belongs")
	}
}

// Metric attributes must be bounded fields only. An identity field here is a series
// per thread, which is the failure this package exists to make impossible.
func TestMetricAttrsCarryNoIdentityField(t *testing.T) {
	g := NewGuard()
	got := g.MetricAttrs(sampleTurn())

	for _, kv := range got {
		if kv.Key == GenAIProvider || kv.Key == GenAIOperation {
			continue // convention-required constants, not Turn-derived
		}
		f, ok := Lookup(kv.Key)
		if !ok {
			t.Errorf("metric attribute %q is not on the contract at all", kv.Key)
			continue
		}
		if f.Class != Bounded {
			t.Errorf("metric attribute %q is %s", kv.Key, f.Class)
		}
	}

	// The two the GenAI convention requires on every one of its instruments.
	for _, k := range []string{GenAIProvider, GenAIOperation} {
		if _, ok := find(got, k); !ok {
			t.Errorf("%s missing; the convention requires it on every gen_ai metric", k)
		}
	}
}

// The whole point of the runtime cap: the corpus says model takes six values, but a
// comment saying so does not stop a field that starts carrying an id from creating a
// series per request.
func TestGuardCapsAFieldThatStartsCarryingIdentifiers(t *testing.T) {
	g := NewGuard()
	f, _ := Lookup(GenAIRequestModel)

	var other int
	for i := range 500 {
		v := g.value(f, fmt.Sprintf("model-%d", i))
		if v == OtherValue {
			other++
		}
	}

	if got := g.Distinct()[GenAIRequestModel]; got != f.Cap {
		t.Errorf("guard admitted %d distinct values, want the cap of %d", got, f.Cap)
	}
	if want := 500 - f.Cap; other != want {
		t.Errorf("%d values collapsed to %s, want %d", other, OtherValue, want)
	}
	if got := g.Rejected()[GenAIRequestModel]; got != int64(500-f.Cap) {
		t.Errorf("rejection counter = %d, want %d - a guard that rewrites silently looks healthy",
			got, 500-f.Cap)
	}
}

// A value already seen must keep flowing after the cap is reached. Otherwise the first
// burst of noise would permanently blind the real value set.
func TestGuardKeepsAdmittingValuesItAlreadyKnows(t *testing.T) {
	g := NewGuard()
	f, _ := Lookup(Status)

	if got := g.value(f, "completed"); got != "completed" {
		t.Fatalf("first sighting = %q", got)
	}
	for i := range f.Cap * 3 {
		g.value(f, fmt.Sprintf("junk-%d", i))
	}
	if got := g.value(f, "completed"); got != "completed" {
		t.Errorf("a known value became %q after the cap filled", got)
	}
}

// With was a bare function and therefore the one path around the guard - and two of
// the three sinks took it, because a tool name and a token type are exactly the
// attributes that cannot be extracted from a Turn. A tool name is whatever the model's
// tool catalogue happens to contain, so it is the likelier explosion, not the safer one.
func TestGuardWithCapsCallerSuppliedValues(t *testing.T) {
	g := NewGuard()
	f, ok := Lookup(ToolName)
	if !ok {
		t.Fatal("tool_name is not on the contract, so nothing caps it")
	}

	var other int
	for i := range 500 {
		got := g.With(nil, KV{ToolName, fmt.Sprintf("tool-%d", i)})
		if len(got) != 1 {
			t.Fatalf("With returned %d attributes for one input", len(got))
		}
		if got[0].Value == OtherValue {
			other++
		}
	}
	if want := 500 - f.Cap; other != want {
		t.Errorf("%d tool names collapsed to %s, want %d", other, OtherValue, want)
	}

	// A key that is not on the contract at all must not ride along unchecked.
	if got := g.With(nil, KV{"codexlb.invented_by_a_sink", "x"}); len(got) != 0 {
		t.Errorf("an off-contract key was emitted: %+v", got)
	}
	if g.Rejected()["codexlb.invented_by_a_sink"] != 1 {
		t.Error("an off-contract key was dropped without being counted")
	}
}

// Guard.With takes an arbitrary caller KV rather than extracting one from a Turn, so
// - unlike SpanAttrs/MetricAttrs/Labels, which walk the registry and skip Sensitive
// fields themselves by construction - it has no class filter of its own unless this
// path exists. Without it, a sink could pass SafetyID's key through With and walk
// straight past every other guarantee this package makes about that field.
func TestGuardWithRejectsSensitiveKeys(t *testing.T) {
	g := NewGuard()
	got := g.With(nil, KV{SafetyID, "a-single-human"})
	if len(got) != 0 {
		t.Fatalf("With emitted a Sensitive key: %+v", got)
	}
	if g.Rejected()[SafetyID] != 1 {
		t.Error("a Sensitive key was dropped without being counted")
	}
}

func TestValidateLabels(t *testing.T) {
	if err := ValidateLabels(DefaultLabels); err != nil {
		t.Fatalf("the shipped default label set does not validate: %v", err)
	}
	if err := ValidateLabels([]string{ServiceName, Family, RecordType, GenAIRequestModel, AccountID}); err != nil {
		t.Errorf("promoting bounded fields was rejected: %v", err)
	}

	for _, bad := range []string{ThreadID, GenAIResponseID, SafetyID, "codexlb.not_a_field"} {
		err := ValidateLabels([]string{ServiceName, bad})
		if err == nil {
			t.Errorf("promoting %q to a Loki label was accepted; that is a stream per value", bad)
			continue
		}
		if !strings.Contains(err.Error(), bad) {
			t.Errorf("error for %q does not name the field: %v", bad, err)
		}
	}
}

// Structured metadata must not repeat what is already a stream label - it is paid on
// every line - but must carry everything that is not.
func TestMetadataExcludesPromotedLabelsAndCarriesTheRest(t *testing.T) {
	g := NewGuard()
	tn := sampleTurn()
	promoted := append(DefaultLabels, GenAIRequestModel)

	md := g.Metadata(tn, promoted)
	if _, ok := find(md, GenAIRequestModel); ok {
		t.Error("model is both a stream label and structured metadata; the bytes are paid twice")
	}
	// The ids that make "why did this call use this model" answerable.
	for _, k := range []string{GenAIResponseID, ThreadID, TurnID, ParentTurnID, RequestID} {
		if _, ok := find(md, k); !ok {
			t.Errorf("%s absent from metadata; a response cannot be looked up without it", k)
		}
	}
}

// Safety buffering re-runs a response through a different model, and it is the only
// case where request and response model legitimately disagree.
func TestResponseModelTracksSafetyRetry(t *testing.T) {
	g := NewGuard()
	tn := sampleTurn()

	got := g.MetricAttrs(tn)
	req, _ := find(got, GenAIRequestModel)
	resp, _ := find(got, GenAIResponseModel)
	if req != resp {
		t.Errorf("with no safety retry, request model %q != response model %q", req, resp)
	}

	tn.SafetyBuffering, tn.SafetyRetryModel = true, "gpt-5.4-mini"
	got = g.MetricAttrs(tn)
	if v, _ := find(got, GenAIResponseModel); v != "gpt-5.4-mini" {
		t.Errorf("response model = %q, want the safety retry model", v)
	}
	if v, _ := find(got, GenAIRequestModel); v != "gpt-5.6-sol" {
		t.Errorf("request model = %q, want the originally requested model", v)
	}
}

// An empty field is omitted, not emitted blank: a blank attribute value is its own
// series and reads as a measurement rather than an absence.
func TestEmptyFieldsAreOmitted(t *testing.T) {
	g := NewGuard()
	got := g.MetricAttrs(&turn.Turn{Model: "gpt-5.6-sol"})

	for _, kv := range got {
		if kv.Value == "" {
			t.Errorf("%q emitted with an empty value", kv.Key)
		}
	}
	if _, ok := find(got, ErrorCode); ok {
		t.Error("error_code emitted on a turn that had no error")
	}
}

// The registry is the contract; a duplicate key would silently shadow a field, and a
// field with no extractor would panic at emit rather than at startup.
func TestRegistryIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Fields() {
		if f.Key == "" {
			t.Error("a field has an empty key")
		}
		if seen[f.Key] {
			t.Errorf("duplicate field key %q", f.Key)
		}
		seen[f.Key] = true
		// Of == nil means caller-supplied (attr.go's own doc comment on Field.Of).
		// Bounded and Identity may both be caller-supplied - a tool name needs
		// capping, a tool-call id or its arguments do not, but either way Guard.With
		// is the one path that reaches them. Sensitive must NEVER be caller-supplied:
		// unlike SpanAttrs/MetricAttrs/Labels, Guard.With has no class-based filter of
		// its own for values it did not extract itself, so a Sensitive field with
		// Of == nil would be the one way to walk safety_identifier-class data around
		// the "never a span attribute" guarantee. (Guard.With rejects Sensitive keys
		// defensively too - see TestGuardWithRejectsSensitiveKeys - but the registry
		// not containing one in the first place is the check that cannot regress.)
		if f.Of == nil && f.Class == Sensitive {
			t.Errorf("%q is caller-supplied (Of == nil) AND Sensitive; Guard.With has no "+
				"class filter, so this is a path around SpanAttrs' Sensitive exclusion", f.Key)
		}
		if f.Class == Bounded && f.Cap <= 0 {
			t.Errorf("%q is bounded with no cap, so nothing stops it growing", f.Key)
		}
		if f.Class != Bounded && f.Cap != 0 {
			t.Errorf("%q is %s but carries a cap, which is never applied", f.Key, f.Class)
		}
	}
	for _, k := range keys(NewGuard().MetricAttrs(sampleTurn())) {
		if k == GenAIProvider || k == GenAIOperation {
			continue
		}
		if !seen[k] {
			t.Errorf("emitted key %q is not in the registry", k)
		}
	}
}
