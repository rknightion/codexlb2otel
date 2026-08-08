package attr

import "testing"

// idJustification is the shared reason for every codexlb.* Identity field below that
// is simply codex-lb's own internal identifier. The GenAI conventions define identity
// at exactly two granularities - gen_ai.conversation.id (the thread) and
// gen_ai.response.id (one model call) - both already mapped to GenAIConversationID and
// GenAIResponseID. Nothing in the convention names a session, a turn, a parent turn, a
// logical turn, a client window, an installation, a prompt-cache key, an engine
// instance or a transport-lifecycle reason; those are this service's own plumbing.
const idJustification = "codex-lb's own internal identifier or lifecycle detail; the " +
	"convention's identity granularity stops at gen_ai.conversation.id / " +
	"gen_ai.response.id, both already mapped"

// semconvJustified is the allowlist TestNoUnjustifiedCodexlbKey checks every codexlb.*
// attribute Field key against, issue #18's acceptance criterion: "a test asserts every
// codexlb.* key we keep has no standard equivalent, so this cannot drift back
// silently." Fields() is live - it is the same slice every sink actually reads - so
// this is not a copy that can go stale on its own; a new codexlb.* field added to the
// registry without a matching entry here fails immediately, and a key removed from the
// registry without removing it here fails just as loudly the other way. Either failure
// forces whoever made the change to look at the standard registry (see names.go's own
// spec-checked-on-date comments for the source and date) and write down why it does
// not apply, rather than the divergence silently drifting back over time.
//
// Every reason below was checked against the live registry (semantic-conventions-genai,
// 2026-08-07), not assumed - see the matching const's own doc comment in names.go for
// the fuller version of each.
var semconvJustified = map[string]string{
	// --- pipeline concepts the convention has no notion of ---
	Status: "answers pipeline-observed-completion, not model finish reason - see the " +
		"Status const's own doc comment for the corpus evidence and why this is a " +
		"different axis from gen_ai.response.finish_reasons, not a coarser version of it",
	RequestKind:          "prewarm/compaction vs a real user turn has no GenAI concept",
	Family:               "websocket/http/probe transport has no GenAI concept",
	FrameType:            "websocket frame classification has no GenAI concept",
	CloseCode:            "websocket close code has no GenAI concept",
	TransportEvent:       "websocket lifecycle prose has no GenAI concept",
	CriticalPathCoverage: "this service's own verdict on its own timing breakdown's trustworthiness",
	BaselineReset:        "this service's own cumulative-delta bookkeeping, not a GenAI concept",
	ErrorCode:            "the provider's own error code enum; error.type (the convention's low-cardinality discriminator) is already mapped as a separate field",

	// --- codex-lb's own load-balancing and account model ---
	AccountID:   "which load-balanced account served the request - no GenAI concept",
	PlanType:    "the subscription tier the rate limits are measured against - no GenAI concept",
	ServiceTier: "codex-lb's default/auto tier - not verified equivalent to openai.response.service_tier (a different, provider-specific namespace); left unmapped rather than guessed",
	// Same provider-specific namespace question as its sibling, plus one the registry
	// does not raise at all: the convention has no notion of a REQUESTED tier as
	// distinct from a served one. openai.response.service_tier is, by its name, the
	// response's - there is no openai.request.service_tier to map this to.
	ServiceTierRequested: "the tier the client asked for, which the convention has no key for at all - openai.response.service_tier names the served value and this is deliberately not that; see the ServiceTierRequested const",
	ThreadSource:         "user vs subagent thread origin - no GenAI concept at this granularity",
	SubagentKind:         "how a subagent thread was created - no GenAI concept",
	Originator:           "the client binary (codex-tui / codex_exec / the probe) - no GenAI concept",

	// --- identifiers and lifecycle detail below gen_ai.conversation.id / gen_ai.response.id ---
	// Forking a conversation is a codex-lb concept, not a model-API one, so there is
	// nothing in the registry to map it to. Its sibling PrevResponseID deliberately
	// does NOT appear in this map, because it is not a codexlb.* key at all: the spec
	// defines gen_ai.request.previous_response.id and names OpenAI's
	// previous_response_id as its own example, so it takes the standard name. That
	// asymmetry between two fields added together is exactly what this test is for.
	ForkedFromThreadID: "forking a conversation into a new thread is codex-lb's own concept - no GenAI equivalent",
	// The one entry here that names a standard key it was deliberately moved OFF,
	// rather than one that never had a standard equivalent. gen_ai.agent.name looks
	// like the match and is not: the spec's span-naming guidance for invoke_agent
	// ("invoke_agent {gen_ai.agent.name}") is about the agent, and codex's spawn_agent
	// TaskName is a per-invocation task label, unbounded by construction. Under the
	// standard key it also collided with the real agent identity, which #32 needed
	// that key for - see GenAIAgentName's own doc comment.
	SubagentTask: "codex's spawn_agent task label - a task description, not an agent " +
		"name, and unbounded per invocation; gen_ai.agent.name carries this service's " +
		"own agent identity instead (issue #32)",

	RequestID:        idJustification,
	SessionID:        idJustification,
	ThreadID:         idJustification,
	ParentThreadID:   idJustification,
	TurnID:           idJustification,
	ParentTurnID:     idJustification,
	LogicalTurnID:    idJustification,
	WindowID:         idJustification,
	InstallationID:   idJustification,
	PromptCacheKey:   idJustification,
	EngineIDs:        idJustification,
	InstructionsHash: idJustification,
	ErrorMessage:     idJustification,

	// --- sensitive ---
	SafetyID: "maps 1:1 to a human; the convention has no notion of an operator identity at all",
}

// TestNoUnjustifiedCodexlbKey is issue #18's acceptance criterion, live against the
// registry rather than a hand-maintained copy of it - see semconvJustified's own doc
// comment for what each direction of mismatch means.
func TestNoUnjustifiedCodexlbKey(t *testing.T) {
	const prefix = "codexlb."
	seen := map[string]bool{}
	for _, f := range Fields() {
		if len(f.Key) < len(prefix) || f.Key[:len(prefix)] != prefix {
			continue
		}
		seen[f.Key] = true
		if _, ok := semconvJustified[f.Key]; !ok {
			t.Errorf("registry field %q is codexlb.* with no entry in semconvJustified - "+
				"either it has a standard gen_ai.* equivalent and should be renamed, or it "+
				"genuinely does not and needs a one-line reason added here", f.Key)
		}
	}
	for k := range semconvJustified {
		if !seen[k] {
			t.Errorf("semconvJustified has a reason for %q, which is no longer in the "+
				"registry - remove the stale entry", k)
		}
	}
}
