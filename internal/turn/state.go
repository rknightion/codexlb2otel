package turn

import "encoding/json"

// State is the Reducer's durable state.
//
// The per-thread cumulative baselines MUST outlive the process. The server's timing
// metrics accumulate across a logical turn, so a Reducer that restarts mid-turn with
// an empty baseline treats the next cumulative reading as if it were a delta - a
// single response would then report the whole turn's tokens and wall time. On a busy
// thread that is a several-hundred-percent overcount on one data point.
//
// In-flight (open) responses are deliberately NOT persisted. A response interrupted
// by a restart never sees its response.completed frame, so it can never be completed
// correctly; carrying it forward would only leak memory. Losing it costs one turn.
type State struct {
	Version int                   `json:"version"`
	Prev    map[string]cumulative `json:"prev"`
	Seq     map[string]int        `json:"seq"`
}

const stateVersion = 1

// cumulative needs exported JSON field names to round-trip through the checkpoint.
func (c cumulative) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		EngineCalls   int     `json:"engine_calls"`
		TurnTimeS     float64 `json:"turn_time_s"`
		SampledTokens int     `json:"sampled_tokens"`
		PromptTokens  int     `json:"prompt_tokens"`
		CachedTokens  int     `json:"cached_tokens"`
		ToolPauseMs   float64 `json:"tool_pause_ms"`
	}{c.engineCalls, c.turnTimeS, c.sampledTokens, c.promptTokens, c.cachedTokens, c.toolPauseMs})
}

func (c *cumulative) UnmarshalJSON(b []byte) error {
	var v struct {
		EngineCalls   int     `json:"engine_calls"`
		TurnTimeS     float64 `json:"turn_time_s"`
		SampledTokens int     `json:"sampled_tokens"`
		PromptTokens  int     `json:"prompt_tokens"`
		CachedTokens  int     `json:"cached_tokens"`
		ToolPauseMs   float64 `json:"tool_pause_ms"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*c = cumulative{v.EngineCalls, v.TurnTimeS, v.SampledTokens, v.PromptTokens, v.CachedTokens, v.ToolPauseMs}
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
