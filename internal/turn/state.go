package turn

import (
	"encoding/json"
	"strings"
	"time"
)

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
	Prev     map[string]cumulative `json:"prev"`
	PrevSeen map[string]time.Time  `json:"prev_seen,omitempty"`
	Seq      map[string]int        `json:"seq"`
	SeqSeen  map[string]time.Time  `json:"seq_seen,omitempty"`
}

// stateVersion 4 adds per-entry archive timestamps. State eviction is deliberately
// age-based, anchored to the newest archive event timestamp seen for the series, not
// the wall clock. Without persisting that anchor, a restart would either evict every
// restored baseline immediately or keep all of them forever.
const stateVersion = 4

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
	s := State{
		Version:  stateVersion,
		Prev:     make(map[string]cumulative, len(r.prev)),
		PrevSeen: make(map[string]time.Time, len(r.prev)),
		Seq:      make(map[string]int, len(r.seq)),
		SeqSeen:  make(map[string]time.Time, len(r.seq)),
	}
	for k, v := range r.prev {
		s.Prev[k] = v
		if ts, ok := r.lastSeen[k]; ok && !ts.IsZero() {
			s.PrevSeen[k] = ts
			thread, _ := splitSeriesKey(k)
			if ts.After(s.SeqSeen[thread]) {
				s.SeqSeen[thread] = ts
			}
		}
	}
	for thread, ts := range r.seqSeen {
		if _, ok := r.seq[thread]; ok && ts.After(s.SeqSeen[thread]) {
			s.SeqSeen[thread] = ts
		}
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
	r.RestoreAt(s, time.Now().UTC())
}

// RestoreAt reinstates a snapshot, using loadedAt as the freshness anchor for old
// snapshots that predate persisted timestamps. That keeps the first pass after an
// upgrade from evicting restored state solely because the checkpoint format was old.
func (r *Reducer) RestoreAt(s State, loadedAt time.Time) {
	if s.Version != stateVersion && s.Version != 3 {
		return
	}
	if s.Prev != nil {
		r.prev = s.Prev
	}
	if s.Seq != nil {
		r.seq = s.Seq
	}
	r.lastSeen = make(map[string]time.Time, len(r.prev))
	r.seqSeen = make(map[string]time.Time, len(r.seq))
	if s.Version == stateVersion {
		for k, ts := range s.PrevSeen {
			if _, ok := r.prev[k]; ok && !ts.IsZero() {
				r.lastSeen[k] = ts
			}
		}
		for thread, ts := range s.SeqSeen {
			if _, ok := r.seq[thread]; ok && !ts.IsZero() {
				r.seqSeen[thread] = ts
			}
		}
		return
	}

	if loadedAt.IsZero() {
		loadedAt = time.Now().UTC()
	}
	for k := range r.prev {
		r.lastSeen[k] = loadedAt
		thread, _ := splitSeriesKey(k)
		r.seqSeen[thread] = loadedAt
	}
	for thread := range r.seq {
		if r.seqSeen[thread].IsZero() {
			r.seqSeen[thread] = loadedAt
		}
	}
}

// StateCounts reports the currently retained reducer state entries.
func (r *Reducer) StateCounts() (series, threads int) {
	return len(r.prev), len(r.seq)
}

// EvictState removes completed reducer baselines older than cutoff. Age is measured
// from the newest archive event timestamp for each cumulative series. Any thread with
// an open response is exempt wholesale: an open response can learn its request_kind
// after creation, so keeping every series for that thread is the safe mid-turn rule.
func (r *Reducer) EvictState(cutoff time.Time) (seriesRemoved, threadsRemoved int) {
	if cutoff.IsZero() {
		return 0, 0
	}
	openThreads := map[string]bool{}
	for _, t := range r.open {
		if t.ThreadID != "" {
			openThreads[t.ThreadID] = true
		}
	}

	liveThreads := map[string]bool{}
	for key := range r.prev {
		thread, _ := splitSeriesKey(key)
		if openThreads[thread] {
			liveThreads[thread] = true
			continue
		}
		if ts, ok := r.lastSeen[key]; ok && !ts.IsZero() && !ts.Before(cutoff) {
			liveThreads[thread] = true
			continue
		}
		delete(r.prev, key)
		delete(r.lastSeen, key)
		seriesRemoved++
	}

	seqSeen := make(map[string]time.Time, len(r.seqSeen))
	for thread, ts := range r.seqSeen {
		seqSeen[thread] = ts
	}
	for key, ts := range r.lastSeen {
		thread, _ := splitSeriesKey(key)
		if ts.After(seqSeen[thread]) {
			seqSeen[thread] = ts
		}
	}
	for thread := range r.seq {
		if openThreads[thread] || liveThreads[thread] {
			continue
		}
		ts := seqSeen[thread]
		if ts.IsZero() || ts.Before(cutoff) {
			delete(r.seq, thread)
			delete(seqSeen, thread)
			threadsRemoved++
		}
	}
	r.seqSeen = seqSeen
	return seriesRemoved, threadsRemoved
}

func splitSeriesKey(key string) (thread, kind string) {
	thread, kind, ok := strings.Cut(key, "\x00")
	if !ok {
		return key, ""
	}
	return thread, kind
}
