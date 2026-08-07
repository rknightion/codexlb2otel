package turn

import "encoding/json"

// State is the Reducer's durable state.
//
// The cumulative baselines MUST outlive the process. The server's timing metrics
// accumulate across a logical turn, so a Reducer that restarts mid-turn with an empty
// baseline treats the next cumulative reading as if it were a delta - a single
// response would then report the whole turn's tokens and wall time. On a busy thread
// that is a several-hundred-percent overcount on one data point.
//
// In-flight (open) responses are deliberately NOT persisted. A response interrupted
// by a restart never sees its response.completed frame, so it can never be completed
// correctly; carrying it forward would only leak memory. Losing it costs one turn.
type State struct {
	Version int `json:"version"`
	// Prev is keyed by turn.seriesKey, NOT by thread id. Version 1 checkpoints keyed it
	// by thread alone; those keys cannot be migrated because the request_kind half was
	// never recorded, so a v1 snapshot is discarded rather than reinterpreted. The cost
	// is one cold-start turn per thread, and cold starts are already flagged.
	Prev map[string]cumulative `json:"prev"`
	Seq  map[string]int        `json:"seq"`
}

// stateVersion 3 adds nine cumulative baselines and the tool-call-duration array
// (issue #12). Unlike the 1 -> 2 change this is not a key change, and a v2 snapshot
// would decode without error - which is exactly why the version has to move anyway.
// Every new counter would restore as zero while the server's own counter sits at its
// real accumulated value, so the first response after a restart would report the
// whole turn's inference and sampling time as its own delta. Worse, `seen` would be
// true, so BaselineReset would NOT fire and the over-count would carry no flag saying
// it was an upper bound. Same silent-over-count shape as #20, arriving through the
// checkpoint instead of the series key. Discarding the snapshot costs one flagged
// cold-start turn per series, which is honest.
const stateVersion = 3

// cumulativeWire is the checkpoint's on-disk shape for cumulative. A named struct
// rather than an inline literal in both Marshal and Unmarshal, because the inline
// form silently invites the two to drift - add a field to one composite literal
// under time pressure and the other typechecks fine while quietly dropping it on
// every restart.
type cumulativeWire struct {
	EngineCalls   int     `json:"engine_calls"`
	TurnTimeS     float64 `json:"turn_time_s"`
	SampledTokens int     `json:"sampled_tokens"`
	PromptTokens  int     `json:"prompt_tokens"`
	CachedTokens  int     `json:"cached_tokens"`
	ToolPauseMs   float64 `json:"tool_pause_ms"`

	ServiceInferenceMs           float64 `json:"service_inference_ms"`
	ServiceSamplingMs            float64 `json:"service_sampling_ms"`
	IapiInferenceMs              float64 `json:"iapi_inference_ms"`
	IapiSamplingMs               float64 `json:"iapi_sampling_ms"`
	ExclEngineAndToolMs          float64 `json:"excl_engine_and_tool_ms"`
	ExclEngineWaitSamplingMs     float64 `json:"excl_engine_wait_sampling_ms"`
	ExclEngineWaitSamplingIapiMs float64 `json:"excl_engine_wait_sampling_iapi_ms"`
	ExclClientToolsMs            float64 `json:"excl_client_tools_ms"`
	UncachedPromptTokens         int     `json:"uncached_prompt_tokens"`

	// ToolCallDurationsMs round-trips the suffix-diff baseline across a restart. Not
	// persisting it would cost more than one turn's accuracy the way losing the
	// scalar baselines would: the NEXT array read after a restart would look like
	// every entry ever accumulated is "newly seen" and re-emit the whole history.
	ToolCallDurationsMs []float64 `json:"tool_call_durations_ms,omitempty"`
}

// cumulative needs exported JSON field names to round-trip through the checkpoint.
func (c cumulative) MarshalJSON() ([]byte, error) {
	return json.Marshal(cumulativeWire{
		EngineCalls:   c.engineCalls,
		TurnTimeS:     c.turnTimeS,
		SampledTokens: c.sampledTokens,
		PromptTokens:  c.promptTokens,
		CachedTokens:  c.cachedTokens,
		ToolPauseMs:   c.toolPauseMs,

		ServiceInferenceMs:           c.serviceInferenceMs,
		ServiceSamplingMs:            c.serviceSamplingMs,
		IapiInferenceMs:              c.iapiInferenceMs,
		IapiSamplingMs:               c.iapiSamplingMs,
		ExclEngineAndToolMs:          c.exclEngineAndToolMs,
		ExclEngineWaitSamplingMs:     c.exclEngineWaitSamplingMs,
		ExclEngineWaitSamplingIapiMs: c.exclEngineWaitSamplingIapiMs,
		ExclClientToolsMs:            c.exclClientToolsMs,
		UncachedPromptTokens:         c.uncachedPromptTokens,

		ToolCallDurationsMs: c.toolCallDurationsMs,
	})
}

func (c *cumulative) UnmarshalJSON(b []byte) error {
	var v cumulativeWire
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*c = cumulative{
		engineCalls:   v.EngineCalls,
		turnTimeS:     v.TurnTimeS,
		sampledTokens: v.SampledTokens,
		promptTokens:  v.PromptTokens,
		cachedTokens:  v.CachedTokens,
		toolPauseMs:   v.ToolPauseMs,

		serviceInferenceMs:           v.ServiceInferenceMs,
		serviceSamplingMs:            v.ServiceSamplingMs,
		iapiInferenceMs:              v.IapiInferenceMs,
		iapiSamplingMs:               v.IapiSamplingMs,
		exclEngineAndToolMs:          v.ExclEngineAndToolMs,
		exclEngineWaitSamplingMs:     v.ExclEngineWaitSamplingMs,
		exclEngineWaitSamplingIapiMs: v.ExclEngineWaitSamplingIapiMs,
		exclClientToolsMs:            v.ExclClientToolsMs,
		uncachedPromptTokens:         v.UncachedPromptTokens,

		toolCallDurationsMs: v.ToolCallDurationsMs,
	}
	return nil
}

// Snapshot captures the state that must survive a restart.
func (r *Reducer) Snapshot() State {
	s := State{Version: stateVersion, Prev: make(map[string]cumulative, len(r.prev)), Seq: make(map[string]int, len(r.seq))}
	for k, v := range r.prev {
		s.Prev[k] = v
	}
	for k, v := range r.seq {
		s.Seq[k] = v
	}
	return s
}

// Restore reinstates a snapshot. A snapshot from an unrecognised version is ignored
// rather than rejected: starting cold costs one turn's accuracy, whereas refusing to
// start costs all of them.
func (r *Reducer) Restore(s State) {
	if s.Version != stateVersion {
		return
	}
	if s.Prev != nil {
		r.prev = s.Prev
	}
	if s.Seq != nil {
		r.seq = s.Seq
	}
}
