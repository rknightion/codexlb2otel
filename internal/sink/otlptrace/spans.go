package otlptrace

import (
	"context"
	"math"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/turn"
)

// The span tree, per Turn (one model response):
//
//	turn        - root span (no OTel parent). One per LogicalTurnID. Represents the
//	              whole logical turn: everything from the client's turn start to the
//	              server finishing the last engine call in it.
//	  response  - child of turn. One per Turn, i.e. per response. This is where
//	              GenAI-convention attributes live and where CriticalPath is checked.
//	    phase   - child of response, one per non-zero CriticalPath phase.
//	    tool_call - child of response, one per ToolCall.
//
// TraceID is ALWAYS derived from ThreadID (see traceID below) - never from the
// archive's own captured traceparent, despite the archive carrying one. That is a
// deliberate deviation, not an oversight: see the comment on traceID.
//
// Turn span id collisions: LogicalTurnID can be shared by more than one response
// when a logical turn makes several engine calls (num_engine_calls > 1). Each such
// response re-emits the SAME turn span id with THIS response's own end time. Start
// is stable across all of them (TurnStart comes from client_metadata sent unchanged
// on every request of the turn), so only End drifts, and it drifts towards the true
// value as later engine calls are processed - the last response processed for that
// turn leaves the most accurate End. This is a known, accepted imprecision: making
// the turn span exactly-once would require buffering across Emit() calls until a
// logical turn is known to be finished, which - short of a wall-clock timeout this
// service has no basis for - cannot be known from a single Turn record. Trace and
// response-span ids are NOT affected: each response's own identity is unique, so a
// response's own spans are emitted exactly once no matter how many engine calls its
// logical turn made.

// traceID is ALWAYS hash(ThreadID) (with fallbacks for the rare turn with no thread
// id at all), even when the archive captured a real W3C traceparent for this
// connection. Two measurements against the live corpus drove that:
//
//  1. The captured traceparent is connection-scoped, not thread-scoped: measured on
//     one archive hour, 8 of 21 threads carried TWO distinct captured trace ids
//     (a mid-life reconnect gets a fresh one). Rooting the trace on it would split
//     a single conversation across two Tempo traces at the reconnect point.
//  2. A subagent thread only ever knows its PARENT's ThreadID (from
//     x-codex-parent-thread-id), never whether the parent's own turns happened to
//     carry a captured traceparent. For the subagent-attachment link below to land
//     on the trace the parent thread actually used, the derivation has to be a pure
//     function of an id the child already has - which the captured traceparent is
//     not, but ThreadID is.
//
// The captured traceparent is not discarded: it becomes a span LINK on the turn
// span (see turnLinks) instead of the trace root, which is what actually satisfies
// "join up with whatever else observed the same request" without either downside.
func traceID(t *turn.Turn) trace.TraceID {
	key := t.ThreadID
	if key == "" {
		key = t.RequestID
	}
	if key == "" {
		key = t.ResponseID
	}
	if key == "" {
		// A genuinely anonymous record - addUncorrelated turns can reach this with no
		// session, thread or request id at all. FirstTS is the archive's own captured
		// timestamp, so this stays a pure function of the record, not of wall-clock
		// processing time.
		key = "ts:" + strconv.FormatInt(t.FirstTS.UnixNano(), 10)
	}
	return hashTraceID("thread", key)
}

// turnKey identifies the logical turn within its thread's trace. LogicalTurnID is
// set by the reducer on every completed/error/transport turn (ensureLogicalTurn),
// so the fallbacks exist only for defence against a Turn built by hand (tests) or a
// future code path that skips that step.
func turnKey(t *turn.Turn) string {
	if t.LogicalTurnID != "" {
		return t.LogicalTurnID
	}
	if t.ResponseID != "" {
		return t.ResponseID
	}
	return t.RequestID
}

func turnSpanID(t *turn.Turn) trace.SpanID {
	return hashSpanID("turn", turnKey(t))
}

// responseKey identifies one response - a Turn - independent of how many other
// responses share its logical turn.
func responseKey(t *turn.Turn) string {
	if t.ResponseID != "" {
		return t.ResponseID
	}
	if t.RequestID != "" {
		return t.RequestID
	}
	return turnKey(t)
}

func responseSpanID(t *turn.Turn) trace.SpanID {
	return hashSpanID("response", responseKey(t))
}

// parentTurnSpanID computes the span id a spawning thread's turn span WOULD have
// (or will have) under this same package's own derivation, purely from the
// identity a subagent's Turn already carries (ParentThreadID, ParentTurnID). No
// lookup, no cross-turn state: whichever process ever emits the parent's matching
// turn will derive this exact id independently, in either order.
func parentTurnSpanID(parentThreadID, parentTurnID string) (trace.TraceID, trace.SpanID) {
	return hashTraceID("thread", parentThreadID), hashSpanID("turn", parentTurnID)
}

// firstNonZero returns the first non-zero time, or the zero Time if all are zero.
func firstNonZero(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// clampEnd guarantees a non-negative span duration. Archive timestamps are captured
// independently (client turn start vs server completion, different clocks) and a
// few milliseconds of skew turning into a negative duration would make some
// exporters reject the span outright rather than merely rendering it oddly.
func clampEnd(start, end time.Time) time.Time {
	if end.Before(start) {
		return start
	}
	return end
}

// toAttrs converts the shared attribute contract's output into the OTel SDK's
// attribute.KeyValue. Every value in attr.KV is already a string (see attr.KV's own
// doc), so this is a straight String() wrap, never a type decision this package
// makes on its own.
func toAttrs(kvs []attr.KV) []attribute.KeyValue {
	out := make([]attribute.KeyValue, len(kvs))
	for i, kv := range kvs {
		out[i] = attribute.String(kv.Key, kv.Value)
	}
	return out
}

// Sink-local span attributes, deliberately NOT in internal/attr.
//
// internal/attr governs values that can become a metric attribute or a Loki label -
// values that get aggregated or index a stream, where an uncontrolled key is a
// cardinality incident. These exist on exactly one span each, describing that span
// alone; nothing sums or groups by them. Keeping them out of the shared registry is
// deliberate, not an oversight - promoting call_id there would put a per-invocation
// id one `attr.With` call away from becoming a label.
const (
	traceAttrLinkKind = "codexlb.trace.link_kind"
	// traceAttrToolCallState has no GenAI standard equivalent - the convention names
	// gen_ai.tool.call.id (issue #18: now attr.ToolCallID, registered and routed
	// through the shared Guard like every other emitted key) but nothing for a call's
	// own status string, so this one stays sink-local.
	traceAttrToolCallState = "codexlb.trace.tool_call_status"
	// traceAttrReconciled records whether the critical-path phase children summed to
	// the response span's own duration within tolerance - the acceptance signal
	// issue #11 asks for, computed rather than merely relayed (CriticalPathCoverage,
	// by contrast, is the server's OWN verdict and already rides in via SpanAttrs).
	traceAttrReconciled = "codexlb.trace.critical_path_reconciled"
)

const (
	linkKindConnection   = "connection_traceparent"
	linkKindSpawningTurn = "spawning_turn"
	// criticalPathTolerance/Floor set how far the summed phase durations may drift
	// from the response span's own duration before it is marked unreconciled: 5% of
	// the response's own duration, or this many ms, whichever is larger - so a 50ms
	// response is not flagged over ordinary millisecond rounding, and a 5-minute one
	// is not given a multi-second free pass.
	criticalPathTolerance = 0.05
	criticalPathFloorMs   = 250.0
)

// turnLinks builds the turn span's links: the archive's own captured traceparent
// when present (external correlation - see the deviation note on traceID), and the
// exact spawning turn when this Turn is a subagent's (ParentThreadID +
// ParentTurnID - the field added specifically so a spawned agent attaches to the
// TURN that spawned it, not to the whole parent thread; see issue #3's comment on
// client_metadata.parent_turn_id).
func turnLinks(t *turn.Turn) []trace.Link {
	var links []trace.Link
	if cid, ok := parseCapturedTraceID(t.TraceID); ok {
		if sid, ok := parseCapturedSpanID(t.SpanID); ok {
			links = append(links, trace.Link{
				SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
					TraceID: cid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true,
				}),
				Attributes: []attribute.KeyValue{attribute.String(traceAttrLinkKind, linkKindConnection)},
			})
		}
	}
	if t.ParentThreadID != "" && t.ParentTurnID != "" {
		ptid, psid := parentTurnSpanID(t.ParentThreadID, t.ParentTurnID)
		links = append(links, trace.Link{
			SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: ptid, SpanID: psid, TraceFlags: trace.FlagsSampled,
			}),
			Attributes: []attribute.KeyValue{attribute.String(traceAttrLinkKind, linkKindSpawningTurn)},
		})
	}
	return links
}

// emitTurn builds and starts/ends the whole span tree for one Turn.
func (s *Sink) emitTurn(ctx context.Context, t *turn.Turn) {
	raw := s.guard.SpanAttrs(t)
	full := toAttrs(raw)

	tid := traceID(t)
	tsid := turnSpanID(t)

	turnStart := firstNonZero(t.TurnStart, t.ClientRequestStart, t.ServerCreatedAt, t.FirstTS)
	if turnStart.IsZero() {
		// Every Turn the reducer emits carries at least FirstTS; a zero here means a
		// hand-built Turn (a test). Anchor rather than hand the SDK a zero time, which
		// some exporters treat as "unset" and reject outright.
		turnStart = time.Unix(0, 0).UTC()
	}

	turnCtx, turnSpan := s.startRoot(ctx, "turn", tid, tsid, turnStart, full, turnLinks(t))

	respStart := firstNonZero(t.ServerCreatedAt, t.FirstTS, turnStart)
	respEnd := clampEnd(respStart, firstNonZero(t.ServerCompletedAt, t.LastTS, respStart))
	rsid := responseSpanID(t)

	respName := "chat"
	if t.Model != "" {
		// gen_ai.operation.name + gen_ai.request.model, per the GenAI convention's
		// span-naming guidance - matches how attr.go aligns naming with the
		// convention wherever it defines the concept.
		respName = "chat " + t.Model
	}
	// gen_ai.operation.name and gen_ai.provider.name go on THIS span and nowhere else.
	//
	// The convention requires both on an inference span, and attr.Guard omits them from
	// SpanAttrs because they are constants rather than Turn-derived fields - which meant
	// no span carried either, and a consumer keying on gen_ai.operation.name saw nothing
	// at all. Grafana's agent-observability backend is exactly such a consumer: it
	// decodes per span and skips anything whose operation name is not one of chat,
	// generate_content, text_completion or invoke_agent.
	//
	// Deliberately NOT added to SpanAttrs, which would put them on the turn root and the
	// critical-path and tool-call children too. A consumer that turns each qualifying
	// span into a record would then count one turn several times over - the parent and
	// its child describing the same inference is a known double-count in that model, so
	// exactly one span per response may claim to be the inference.
	respAttrs := append(full[:len(full):len(full)],
		attribute.String(attr.GenAIOperation, attr.GenAIOperationValue),
		attribute.String(attr.GenAIProvider, attr.GenAIProviderValue),
	)
	respCtx, respSpan := s.startChild(turnCtx, turnSpan.SpanContext(), respName, rsid, respStart, respAttrs)

	rkey := responseKey(t)
	reconciled, hasCriticalPath := s.emitCriticalPathPhases(respCtx, respSpan.SpanContext(), raw, rkey, t.CriticalPath, respStart)
	if hasCriticalPath {
		respSpan.SetAttributes(attribute.Bool(traceAttrReconciled, reconciled))
	}
	s.emitToolCalls(respCtx, respSpan.SpanContext(), raw, rkey, t.ToolCalls, t.ToolOutputs, respStart, respEnd)

	respSpan.End(trace.WithTimestamp(respEnd))
	// The turn span's own end tracks the LATEST response processed for this logical
	// turn - see the package doc comment above on turn-span-id collisions.
	turnSpan.End(trace.WithTimestamp(clampEnd(turnStart, respEnd)))
}

// emitCriticalPathPhases lays the server's own per-response timing breakdown out as
// sequential child spans and reports whether they reconcile with the response's own
// duration. hasCriticalPath is false when the response carries no critical-path data
// at all, in which case reconciled is meaningless and the caller must not emit the
// attribute - "no data" and "data that doesn't add up" are different findings, and
// collapsing them would hide the former inside a false "false".
func (s *Sink) emitCriticalPathPhases(ctx context.Context, parent trace.SpanContext, raw []attr.KV, respKey string, cp turn.CriticalPath, respStart time.Time) (reconciled, hasCriticalPath bool) {
	phases := []struct {
		name string
		ms   float64
	}{
		{"pre_inference", cp.PreInferenceMs},
		{"engine_wall", cp.EngineWallMs},
		{"sampling_and_stream", cp.SamplingStreamMs},
		{"client_tool_pause", cp.ClientToolPauseMs},
		{"other", cp.OtherMs},
	}
	var sumMs float64
	for _, p := range phases {
		sumMs += p.ms
	}
	if sumMs <= 0 && cp.Coverage == "" {
		return false, false
	}

	attrs := toAttrs(attr.Only(raw, attr.CriticalPathCoverage, attr.GenAIResponseID, attr.ThreadID, attr.Status))
	cursor := respStart
	for _, p := range phases {
		if p.ms <= 0 {
			continue
		}
		end := cursor.Add(time.Duration(p.ms * float64(time.Millisecond)))
		sid := hashSpanID("phase", respKey, p.name)
		_, span := s.startChild(ctx, parent, "critical_path."+p.name, sid, cursor, attrs)
		span.End(trace.WithTimestamp(end))
		cursor = end
	}

	elapsedMs := float64(cursor.Sub(respStart)) / float64(time.Millisecond)
	tolerance := math.Max(criticalPathFloorMs, criticalPathTolerance*elapsedMs)
	reconciled = cp.Coverage != "missing_harness_boundary" && math.Abs(sumMs-elapsedMs) <= tolerance
	return reconciled, true
}

// emitToolCalls adds one child span per tool invocation. Turn.ToolCall carries no
// per-call timing (the archive's responsesapi_duration_excl_engine_per_tool_call_ms
// array is not currently reduced into Turn - that is internal/turn's scope, not
// this sink's), so every tool-call span spans the FULL response duration rather
// than a false narrower interval. That is a stated limitation, not a silent
// approximation: a future internal/turn change that captures per-call timing needs
// no change here beyond narrowing start/end.
//
// gen_ai.operation.name goes on these spans too (issue #18), execute_tool or
// invoke_agent depending on the call - see toolOperationName below. This does NOT
// reopen the double-count trap fixed in 228c717: that fix bounds how many spans per
// RESPONSE may claim to be the "chat" inference (exactly one, the response span
// itself). execute_tool/invoke_agent are a DIFFERENT operation value describing a
// DIFFERENT event - one tool call, one span, one claim - not a second span claiming
// the same inference the response span already claimed. A consumer counting "chat"
// spans to count inferences and "execute_tool"/"invoke_agent" spans to count tool
// calls/agent invocations gets each number counted once, on its own axis.
func (s *Sink) emitToolCalls(ctx context.Context, parent trace.SpanContext, raw []attr.KV, respKey string, calls []turn.ToolCall, outputs []turn.ToolOutput, respStart, respEnd time.Time) {
	if len(calls) == 0 {
		return
	}
	// Joined by call id, matching how the reducer itself associates a tool's result
	// with its call (turn.ToolOutput's own doc comment: "deduplicated by call id").
	outputByCallID := make(map[string]string, len(outputs))
	for _, o := range outputs {
		if o.CallID != "" {
			outputByCallID[o.CallID] = o.Text
		}
	}

	base := attr.Only(raw, attr.GenAIResponseID, attr.ThreadID, attr.GenAIRequestModel, attr.Status)
	for i, tc := range calls {
		op := toolOperationName(tc)
		attrs := s.guard.With(base,
			attr.KV{Key: attr.ToolName, Value: tc.Name},
			attr.KV{Key: attr.ToolCallID, Value: tc.CallID},
			attr.KV{Key: attr.ToolType, Value: tc.Kind},
			attr.KV{Key: attr.ToolCallArguments, Value: tc.Input},
			attr.KV{Key: attr.ToolCallResult, Value: outputByCallID[tc.CallID]},
			// GenAIAgentName only actually has a value when op is invoke_agent -
			// TaskName is empty for every other tool - so this is a no-op KV on an
			// execute_tool call rather than a branch.
			attr.KV{Key: attr.GenAIAgentName, Value: tc.TaskName},
		)
		otelAttrs := toAttrs(attrs)
		// gen_ai.operation.name: constant per call, not Turn-derived and not
		// caller-supplied-but-registered either (there is no Field for "which
		// operation value" - GenAIOperation itself is injected the same way on the
		// chat span, deliberately outside the registry; see that span's own comment).
		otelAttrs = append(otelAttrs, attribute.String(attr.GenAIOperation, op))
		if tc.Status != "" {
			otelAttrs = append(otelAttrs, attribute.String(traceAttrToolCallState, tc.Status))
		}
		sid := hashSpanID("tool_call", respKey, strconv.Itoa(i), tc.CallID, tc.Name)
		// Span name per the spec's own guidance for each operation: "execute_tool
		// {gen_ai.tool.name}" / "invoke_agent {gen_ai.agent.name}" - the latter uses
		// TaskName (the spawned agent's own name), not tc.Name (which would just say
		// "spawn_agent" on every one of these spans and tell a reader nothing about
		// which agent).
		name := op
		switch {
		case op == attr.GenAIOperationInvokeAgent && tc.TaskName != "":
			name = op + " " + tc.TaskName
		case tc.Name != "":
			name = op + " " + tc.Name
		}
		_, span := s.startChild(ctx, parent, name, sid, respStart, otelAttrs)
		span.End(trace.WithTimestamp(respEnd))
	}
}

// toolOperationName picks execute_tool or invoke_agent per the GenAI convention's own
// two operation values for this event class.
//
// TaskName is "Populated for spawn_agent, describing the child agent" (turn.ToolCall's
// own doc comment) - it is Turn's existing, already-reduced signal for "this call
// spawned a subagent", so it is used directly rather than hardcoding the tool name
// "spawn_agent" a second time in this package. Every other multi-agent-protocol tool
// this service has observed (wait_agent, list_agents, send_message, followup_task,
// interrupt_agent - see ToolName's Observed list) interacts with an ALREADY-spawned
// agent rather than creating one, so those stay execute_tool; only the call that
// actually brings a new agent into existence is invoke_agent.
func toolOperationName(tc turn.ToolCall) string {
	if tc.TaskName != "" {
		return attr.GenAIOperationInvokeAgent
	}
	return attr.GenAIOperationExecuteTool
}
