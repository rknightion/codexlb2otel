package otlptrace

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/fixture"
	"github.com/rknightion/codexlb2otel/internal/frame"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// newTestSink builds a Sink around an in-memory exporter rather than a real OTLP
// HTTP endpoint - the corpus tests exercise the span TREE this package builds, not
// the network path, which New's caller (the wiring pass) is responsible for and
// which the "do not export to Grafana Cloud from tests" instruction rules out here
// anyway. WithSyncer rather than WithBatcher: tests want every span exported the
// instant it ends, with no batching window to race against.
func newTestSink(tb testing.TB) (*Sink, *tracetest.InMemoryExporter) {
	tb.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
		sdktrace.WithIDGenerator(pinnedIDGenerator{}),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	tb.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return &Sink{
		tp:       tp,
		tracer:   tp.Tracer("test"),
		guard:    attr.NewGuard(),
		rejected: map[string]int64{},
	}, exp
}

// --- corpus plumbing, mirroring internal/turn/reducer_test.go's own pattern ---

func reduceFiles(tb testing.TB, paths ...string) []*turn.Turn {
	tb.Helper()
	r := turn.New()
	var out []*turn.Turn
	for _, p := range paths {
		b := fixture.Load(tb, p)
		res, err := archive.DecodeMembers(b)
		if err != nil {
			tb.Fatalf("%s: %v", fixture.Name(p), err)
		}
		err = frame.Lines(res.Data, func(rec *frame.Record) error {
			done, err := r.Add(rec)
			if done != nil {
				out = append(out, done)
			}
			return err
		})
		if err != nil {
			tb.Fatalf("%s: %v", fixture.Name(p), err)
		}
	}
	return append(out, r.Flush()...)
}

// reduceUntil feeds archives, cheapest first, until want() is satisfied against the
// accumulated turns - stopping there rather than reducing the whole corpus, since
// some shapes (a real subagent with a parent turn id, a captured traceparent) are
// rare and cost real time to find by brute force otherwise. Exhausting the corpus
// without the property is a hard failure: it means this test is silently untested,
// not that the property doesn't matter.
func reduceUntil(tb testing.TB, what string, want func([]*turn.Turn) bool) []*turn.Turn {
	tb.Helper()
	files := fixture.Files(tb)
	r := turn.New()
	var out []*turn.Turn
	for i, p := range files {
		b := fixture.Load(tb, p)
		res, err := archive.DecodeMembers(b)
		if err != nil {
			tb.Fatalf("%s: %v", fixture.Name(p), err)
		}
		err = frame.Lines(res.Data, func(rec *frame.Record) error {
			done, err := r.Add(rec)
			if done != nil {
				out = append(out, done)
			}
			return err
		})
		if err != nil {
			tb.Fatalf("%s: %v", fixture.Name(p), err)
		}
		if want(out) {
			tb.Logf("%s found after %d/%d archives", what, i+1, len(files))
			return append(out, r.Flush()...)
		}
	}
	out = append(out, r.Flush()...)
	if !want(out) {
		tb.Fatalf("the corpus under %s does not contain %s (%d archives, %d turns)",
			fixture.Root(tb), what, len(files), len(out))
	}
	return out
}

// --- determinism (acceptance criterion: re-processing is idempotent) ---

func TestDeterminism_SameTurnTwiceYieldsIdenticalIDs(t *testing.T) {
	// Asks for the turns it needs rather than for two archives that used to happen to
	// hold them. `fixture.Any(t, 2)` takes the two CHEAPEST archives, so what this
	// test reduces depends on the size distribution of whatever is in the corpus - and
	// on 2026-08-08 a sync brought in two hours of 360KB and 530KB, which between them
	// yielded 7 turns and failed the check below on a corpus that was entirely
	// healthy. The property wanted here is only "enough turns for a meaningful
	// comparison"; reduceUntil expresses that directly and keeps reading archives
	// until it is true.
	turns := reduceUntil(t, "at least 20 turns to compare across two runs",
		func(ts []*turn.Turn) bool { return len(ts) >= 20 })

	run := func() tracetest.SpanStubs {
		s, exp := newTestSink(t)
		if err := s.Emit(context.Background(), turns); err != nil {
			t.Fatalf("Emit: %v", err)
		}
		if err := s.Flush(context.Background()); err != nil {
			t.Fatalf("Flush: %v", err)
		}
		return exp.GetSpans()
	}

	first := run()
	second := run()
	if len(first) == 0 {
		t.Fatal("no spans exported; corpus/derivation looks broken")
	}
	if len(first) != len(second) {
		t.Fatalf("span count differs across identical runs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		a, b := first[i], second[i]
		if a.SpanContext.TraceID() != b.SpanContext.TraceID() {
			t.Fatalf("span %d (%s): trace id differs across runs: %s vs %s", i, a.Name, a.SpanContext.TraceID(), b.SpanContext.TraceID())
		}
		if a.SpanContext.SpanID() != b.SpanContext.SpanID() {
			t.Fatalf("span %d (%s): span id differs across runs: %s vs %s", i, a.Name, a.SpanContext.SpanID(), b.SpanContext.SpanID())
		}
		if !a.StartTime.Equal(b.StartTime) {
			t.Fatalf("span %d (%s): start time differs across runs: %s vs %s", i, a.Name, a.StartTime, b.StartTime)
		}
	}
}

// --- no zero id, ever (acceptance criterion) ---

func TestNoZeroID(t *testing.T) {
	turns := reduceFiles(t, fixture.Any(t, 2)...)
	s, exp := newTestSink(t)
	if err := s.Emit(context.Background(), turns); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	spans := exp.GetSpans()
	if len(spans) == 0 {
		t.Fatal("no spans exported")
	}
	for _, sp := range spans {
		if !sp.SpanContext.TraceID().IsValid() {
			t.Fatalf("span %q has an invalid (zero) trace id", sp.Name)
		}
		if !sp.SpanContext.SpanID().IsValid() {
			t.Fatalf("span %q has an invalid (zero) span id", sp.Name)
		}
	}
}

// --- timestamps are historical, not export time ---

func TestTimestampsAreHistorical(t *testing.T) {
	turns := reduceFiles(t, fixture.Any(t, 1)...)
	if len(turns) == 0 {
		t.Fatal("no turns")
	}
	before := time.Now()
	s, exp := newTestSink(t)
	if err := s.Emit(context.Background(), turns); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	// The corpus can be recent (captured hours before a test run, not years), so
	// wall-clock age alone cannot distinguish "historical" from "export time" - the
	// two can fall on the same calendar day. What DOES distinguish them: export
	// happens strictly AFTER the call to Flush above, so a start time equal to the
	// turn's own FirstTS (set from the archive, long before this test ran) can never
	// be later than "before", whereas time.Now() at export would be.
	for _, sp := range exp.GetSpans() {
		if sp.StartTime.After(before) {
			t.Fatalf("span %q StartTime %s is AFTER this test called Flush (%s) - looks like export time, not archive time",
				sp.Name, sp.StartTime, before)
		}
		if sp.EndTime.Before(sp.StartTime) {
			t.Fatalf("span %q has EndTime before StartTime: %s < %s", sp.Name, sp.EndTime, sp.StartTime)
		}
	}
}

// --- parent-child linkage: response is a child of turn ---

func TestParentChildLinkage(t *testing.T) {
	turns := reduceFiles(t, fixture.Any(t, 2)...)
	s, exp := newTestSink(t)
	if err := s.Emit(context.Background(), turns); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	spans := exp.GetSpans()

	byID := make(map[trace.SpanID]tracetest.SpanStub, len(spans))
	for _, sp := range spans {
		byID[sp.SpanContext.SpanID()] = sp
	}

	found := 0
	for _, sp := range spans {
		if sp.Name == "turn" {
			continue // turn spans are roots by construction; nothing to check here
		}
		if !sp.Parent.IsValid() {
			continue // top-level spans this test isn't checking
		}
		parent, ok := byID[sp.Parent.SpanID()]
		if !ok {
			continue // parent wasn't exported in this batch (e.g. cross-trace link target)
		}
		if sp.Parent.TraceID() != sp.SpanContext.TraceID() {
			t.Fatalf("span %q has a parent in a DIFFERENT trace (%s vs %s) - true nesting requires the same trace",
				sp.Name, sp.Parent.TraceID(), sp.SpanContext.TraceID())
		}
		found++
		_ = parent
	}
	if found == 0 {
		t.Fatal("no parent-child relationships observed at all; the tree isn't being built")
	}
}

// --- subagent attachment (acceptance criterion) ---

func TestSubagentAttachesToSpawningTurn(t *testing.T) {
	turns := reduceUntil(t, "a subagent turn with ParentThreadID and ParentTurnID", func(ts []*turn.Turn) bool {
		for _, tt := range ts {
			if tt.ParentThreadID != "" && tt.ParentTurnID != "" {
				return true
			}
		}
		return false
	})

	var subject *turn.Turn
	for _, tt := range turns {
		if tt.ParentThreadID != "" && tt.ParentTurnID != "" {
			subject = tt
			break
		}
	}

	s, exp := newTestSink(t)
	if err := s.Emit(context.Background(), []*turn.Turn{subject}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	wantTID, wantSID := parentTurnSpanID(subject.ParentThreadID, subject.ParentTurnID)

	var turnSpan tracetest.SpanStub
	for _, sp := range exp.GetSpans() {
		if sp.Name == "turn" {
			turnSpan = sp
			break
		}
	}
	if turnSpan.Name == "" {
		t.Fatal("no turn span exported")
	}

	var found bool
	for _, l := range turnSpan.Links {
		if l.SpanContext.TraceID() == wantTID && l.SpanContext.SpanID() == wantSID {
			found = true
		}
	}
	if !found {
		t.Fatalf("turn span for subagent thread %s has no link to the spawning turn (thread %s, turn %s); links: %+v",
			subject.ThreadID, subject.ParentThreadID, subject.ParentTurnID, turnSpan.Links)
	}

	// Independently, the exact same identity ALWAYS resolves to the same target -
	// whichever order the parent and child are processed in, which is what makes
	// the attachment work without ever having seen the parent's own turn.
	tid2, sid2 := parentTurnSpanID(subject.ParentThreadID, subject.ParentTurnID)
	if tid2 != wantTID || sid2 != wantSID {
		t.Fatalf("parentTurnSpanID is not deterministic: (%s,%s) vs (%s,%s)", wantTID, wantSID, tid2, sid2)
	}
}

// --- captured traceparent becomes a link, never the trace root (documented deviation) ---

func TestCapturedTraceparentBecomesLinkNotRoot(t *testing.T) {
	turns := reduceUntil(t, "a turn with a captured traceparent", func(ts []*turn.Turn) bool {
		for _, tt := range ts {
			if tt.TraceID != "" && tt.SpanID != "" {
				return true
			}
		}
		return false
	})

	var subject *turn.Turn
	for _, tt := range turns {
		if tt.TraceID != "" && tt.SpanID != "" {
			subject = tt
			break
		}
	}

	capturedTID, ok := parseCapturedTraceID(subject.TraceID)
	if !ok {
		t.Fatalf("corpus traceparent %q did not parse as a valid W3C trace id", subject.TraceID)
	}

	s, exp := newTestSink(t)
	if err := s.Emit(context.Background(), []*turn.Turn{subject}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var turnSpan tracetest.SpanStub
	for _, sp := range exp.GetSpans() {
		if sp.Name == "turn" {
			turnSpan = sp
		}
	}
	if turnSpan.Name == "" {
		t.Fatal("no turn span exported")
	}

	if turnSpan.SpanContext.TraceID() == capturedTID {
		t.Fatalf("turn span used the captured traceparent as its trace id; " +
			"it must be derived from ThreadID instead (see the deviation note on traceID in spans.go) " +
			"and the captured id attached as a link")
	}
	if got := traceID(subject); got != turnSpan.SpanContext.TraceID() {
		t.Fatalf("turn span trace id %s does not match hash(ThreadID) %s", turnSpan.SpanContext.TraceID(), got)
	}

	var linked bool
	for _, l := range turnSpan.Links {
		if l.SpanContext.TraceID() == capturedTID {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("captured traceparent %s was not attached as a link on the turn span", capturedTID)
	}
}

// --- critical-path phases sum to the response span within tolerance, or are marked ---

func TestCriticalPathReconciliation(t *testing.T) {
	turns := reduceUntil(t, "a turn with complete critical-path coverage", func(ts []*turn.Turn) bool {
		for _, tt := range ts {
			if tt.CriticalPath.Coverage == "complete" && tt.CriticalPath.EngineWallMs > 0 {
				return true
			}
		}
		return false
	})

	var subject *turn.Turn
	for _, tt := range turns {
		if tt.CriticalPath.Coverage == "complete" && tt.CriticalPath.EngineWallMs > 0 {
			subject = tt
			break
		}
	}

	s, exp := newTestSink(t)
	if err := s.Emit(context.Background(), []*turn.Turn{subject}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var respSpan tracetest.SpanStub
	var phaseCount int
	for _, sp := range exp.GetSpans() {
		if sp.Parent.IsValid() {
			if sp.Name != "" && len(sp.Name) > len("critical_path.") && sp.Name[:len("critical_path.")] == "critical_path." {
				phaseCount++
			}
		}
		for _, a := range sp.Attributes {
			if a.Key == traceAttrReconciled {
				respSpan = sp
			}
		}
	}
	if respSpan.Name == "" {
		t.Fatal("no response span carried the reconciliation attribute")
	}
	if phaseCount == 0 {
		t.Fatal("no critical-path phase spans were emitted")
	}
	t.Logf("response %q: %d phase children, reconciled attribute present", respSpan.Name, phaseCount)
}

// --- tool calls become child spans ---

func TestToolCallSpans(t *testing.T) {
	turns := reduceUntil(t, "a turn with at least one tool call", func(ts []*turn.Turn) bool {
		for _, tt := range ts {
			if len(tt.ToolCalls) > 0 {
				return true
			}
		}
		return false
	})

	var subject *turn.Turn
	for _, tt := range turns {
		if len(tt.ToolCalls) > 0 {
			subject = tt
			break
		}
	}

	s, exp := newTestSink(t)
	if err := s.Emit(context.Background(), []*turn.Turn{subject}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var toolSpans int
	for _, sp := range exp.GetSpans() {
		for _, a := range sp.Attributes {
			if a.Key == attr.ToolName {
				toolSpans++
			}
		}
	}
	if toolSpans != len(subject.ToolCalls) {
		t.Fatalf("got %d tool-call spans, want %d (matching Turn.ToolCalls)", toolSpans, len(subject.ToolCalls))
	}
}

// --- temperature/top_p (issue #23): response span only, correct GenAI names ---

func TestTemperatureTopPOnResponseSpanOnly(t *testing.T) {
	s, exp := newTestSink(t)
	now := time.Now()
	tt := &turn.Turn{
		RequestID: "req-1", ResponseID: "resp-1", ThreadID: "thread-1",
		Model: "gpt-5.6-sol", Status: "completed",
		FirstTS: now.Add(-time.Second), LastTS: now,
		ServerCreatedAt: now.Add(-time.Second), ServerCompletedAt: now,
		Temperature: 1.0, TopP: 0.98,
	}
	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var respSpan tracetest.SpanStub
	for _, sp := range exp.GetSpans() {
		if sp.Name == "generateText gpt-5.6-sol" {
			respSpan = sp
		}
	}
	if respSpan.Name == "" {
		t.Fatal("no response (generateText) span exported")
	}

	var gotTemp, gotTopP float64
	var hasTemp, hasTopP bool
	for _, a := range respSpan.Attributes {
		switch string(a.Key) {
		case attr.GenAIRequestTemperature:
			hasTemp, gotTemp = true, a.Value.AsFloat64()
		case attr.GenAIRequestTopP:
			hasTopP, gotTopP = true, a.Value.AsFloat64()
		}
	}
	if !hasTemp || gotTemp != 1.0 {
		t.Errorf("response span %s = %v (present=%v), want 1.0", attr.GenAIRequestTemperature, gotTemp, hasTemp)
	}
	if !hasTopP || gotTopP != 0.98 {
		t.Errorf("response span %s = %v (present=%v), want 0.98", attr.GenAIRequestTopP, gotTopP, hasTopP)
	}

	// Never on any other span (turn root, phase children, tool_call children) - same
	// single-claimant scoping GenAIOperation/GenAIProvider already have on this span.
	for _, sp := range exp.GetSpans() {
		if sp.Name == "generateText gpt-5.6-sol" {
			continue
		}
		for _, a := range sp.Attributes {
			if string(a.Key) == attr.GenAIRequestTemperature || string(a.Key) == attr.GenAIRequestTopP {
				t.Errorf("span %q carries %s, want it scoped to the response span only", sp.Name, a.Key)
			}
		}
	}
}

func TestResponseSpanKindAndStatusMatchAgentO11ySDK(t *testing.T) {
	for _, tc := range []struct {
		name       string
		errorType  string
		errorMsg   string
		wantStatus codes.Code
		wantEvent  bool
	}{
		{name: "success", wantStatus: codes.Ok},
		{name: "error", errorType: "rate_limit", errorMsg: "too many requests", wantStatus: codes.Error, wantEvent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, exp := newTestSink(t)
			now := time.Now()
			tt := &turn.Turn{
				RequestID: "req-1", ResponseID: "resp-1", ThreadID: "thread-1",
				Model: "gpt-5.6-sol", Status: "completed",
				FirstTS: now.Add(-time.Second), LastTS: now,
				ServerCreatedAt: now.Add(-time.Second), ServerCompletedAt: now,
				ErrorType: tc.errorType, ErrorMessage: tc.errorMsg,
			}
			if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
				t.Fatal(err)
			}
			var got tracetest.SpanStub
			for _, sp := range exp.GetSpans() {
				if sp.Name == "generateText gpt-5.6-sol" {
					got = sp
				}
			}
			if got.Name == "" {
				t.Fatal("response span not exported")
			}
			if got.SpanKind != trace.SpanKindClient {
				t.Errorf("SpanKind = %v, want CLIENT", got.SpanKind)
			}
			if got.Status.Code != tc.wantStatus {
				t.Errorf("status = %v, want %v", got.Status.Code, tc.wantStatus)
			}
			var hasException bool
			for _, event := range got.Events {
				hasException = hasException || event.Name == "exception"
			}
			if hasException != tc.wantEvent {
				t.Errorf("exception event = %v, want %v", hasException, tc.wantEvent)
			}
		})
	}
}

// --- tool-call durations (issue #23): narrow the span, and guard the length mismatch ---

func TestToolCallDurations_NarrowSpanEnd(t *testing.T) {
	s, exp := newTestSink(t)
	now := time.Now()
	start := now.Add(-10 * time.Second)
	tt := &turn.Turn{
		RequestID: "req-1", ResponseID: "resp-1", ThreadID: "thread-1",
		Model: "gpt-5.6-sol", Status: "completed",
		FirstTS: start, LastTS: now,
		ServerCreatedAt: start, ServerCompletedAt: now,
		ToolCalls: []turn.ToolCall{
			{Kind: "custom", Name: "exec", CallID: "c1"},
			{Kind: "custom", Name: "wait", CallID: "c2"},
		},
		// Both durations are well under the 10s response - if the span were still
		// spanning the full response (the pre-#23 approximation), these checks below
		// would see 10s on both, not 0.5s/1.5s.
		ToolCallDurationsMs: []float64{500, 1500},
	}
	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	toolSpans := toolCallSpans(exp.GetSpans())
	if len(toolSpans) != 2 {
		t.Fatalf("got %d tool-call spans, want 2", len(toolSpans))
	}
	for _, sp := range toolSpans {
		name := toolNameOf(sp)
		wantDur := 500 * time.Millisecond
		if name == "wait" {
			wantDur = 1500 * time.Millisecond
		}
		if gotDur := sp.EndTime.Sub(sp.StartTime); gotDur != wantDur {
			t.Errorf("tool call %q span duration = %v, want %v (narrowed to ToolCallDurationsMs, "+
				"not the full 10s response)", name, gotDur, wantDur)
		}
	}
}

func TestToolCallDurations_LengthMismatchFallsBackToFullResponse(t *testing.T) {
	s, exp := newTestSink(t)
	now := time.Now()
	start := now.Add(-10 * time.Second)
	tt := &turn.Turn{
		RequestID: "req-1", ResponseID: "resp-1", ThreadID: "thread-1",
		Model: "gpt-5.6-sol", Status: "completed",
		FirstTS: start, LastTS: now,
		ServerCreatedAt: start, ServerCompletedAt: now,
		ToolCalls: []turn.ToolCall{
			{Kind: "custom", Name: "exec", CallID: "c1"},
			{Kind: "custom", Name: "wait", CallID: "c2"},
		},
		// Length 1 against 2 ToolCalls: a genuine mismatch between the two
		// independently-decoded wire arrays, not something index 0 can be trusted to
		// mean "exec's duration" against.
		ToolCallDurationsMs: []float64{500},
	}
	if err := s.Emit(context.Background(), []*turn.Turn{tt}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	wantDur := now.Sub(start)
	toolSpans := toolCallSpans(exp.GetSpans())
	if len(toolSpans) != 2 {
		t.Fatalf("got %d tool-call spans, want 2", len(toolSpans))
	}
	for _, sp := range toolSpans {
		if gotDur := sp.EndTime.Sub(sp.StartTime); gotDur != wantDur {
			t.Errorf("tool call %q span duration = %v, want %v (full-response fallback on a "+
				"length mismatch, not a guess at which entries align)", toolNameOf(sp), gotDur, wantDur)
		}
	}
}

func toolCallSpans(spans tracetest.SpanStubs) []tracetest.SpanStub {
	var out []tracetest.SpanStub
	for _, sp := range spans {
		for _, a := range sp.Attributes {
			if a.Key == attr.ToolName {
				out = append(out, sp)
				break
			}
		}
	}
	return out
}

func toolNameOf(sp tracetest.SpanStub) string {
	for _, a := range sp.Attributes {
		if a.Key == attr.ToolName {
			return a.Value.AsString()
		}
	}
	return ""
}

// --- Flush surfaces export failure, and does not silently swallow it ---

func TestFlushReportsExportFailure(t *testing.T) {
	turns := reduceFiles(t, fixture.Any(t, 1)...)
	if len(turns) == 0 {
		t.Fatal("no turns")
	}
	// WithBatcher, matching production (New uses WithBatcher too): OnEnd only
	// enqueues, and ForceFlush is what actually calls ExportSpans and returns its
	// error. WithSyncer exports (and would swallow the error) at span.End() time,
	// before Flush ever runs, so it cannot exercise the path this test is for.
	failing := &failingExporter{}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(failing),
		sdktrace.WithIDGenerator(pinnedIDGenerator{}),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	s := &Sink{tp: tp, tracer: tp.Tracer("test"), guard: attr.NewGuard(), rejected: map[string]int64{}}

	if err := s.Emit(context.Background(), turns[:1]); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := s.Flush(context.Background()); err == nil {
		t.Fatal("Flush did not report the exporter's failure")
	}
	rej := s.Rejections()
	if len(rej) != 1 || rej[0].Reason != "transport" || rej[0].Count < 1 {
		t.Fatalf("Rejections() = %+v, want one transport rejection", rej)
	}
}

type failingExporter struct{}

func (failingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errAlwaysFails
}
func (failingExporter) Shutdown(context.Context) error { return nil }

var errAlwaysFails = errTest("simulated export failure")

type errTest string

func (e errTest) Error() string { return string(e) }
