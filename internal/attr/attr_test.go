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
		if f.Of == nil {
			t.Errorf("%q has no extractor", f.Key)
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
