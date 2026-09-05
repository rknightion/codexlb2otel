package turn

import "time"

// callRef identifies an observed invocation; CapturedAt is archive observation time.
type callRef struct {
	ResponseID string    `json:"response_id"`
	TurnID     string    `json:"turn_id"`
	ToolName   string    `json:"tool_name"`
	CapturedAt time.Time `json:"captured_at"`
}

// callIndex is the wave-zero seam. L2 implements bounded checkpointed correlation.
// advance supplies the archive clock without adding a wall-clock lookup parameter.
type callIndex struct{}

func (*callIndex) advance(time.Time)                              {}
func (*callIndex) record(thread, callID string, ref callRef)      {}
func (*callIndex) lookup(thread, callID string) (callRef, string) { return callRef{}, "none" }
