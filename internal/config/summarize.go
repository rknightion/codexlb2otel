package config

import (
	"fmt"
	"strings"
)

// Validate reports every problem with the summarize block, or nil.
//
// Separate from Config.Validate because the two answer different questions. Config.Validate
// enforces what the SERVICE needs - at least one credentialed sink - since a daemon that
// tails the archive and ships it nowhere is a silent no-op. clbsum runs no pipeline, so it
// validates only this block; the daemon folds it in as one part of a wider check.
//
// Enforced only when Enabled. The block is present in every default config, so validating
// it unconditionally would fail every deployment that never asked for the feature.
func (s Summarize) Validate() error {
	if !s.Enabled {
		return nil
	}
	problems := s.problems()
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid summarize configuration:\n  - %s", strings.Join(problems, "\n  - "))
}

// problems lists what is wrong, so Config.Validate can fold these in beside its own rather
// than nesting one error message inside another.
func (s Summarize) problems() []string {
	if !s.Enabled {
		return nil
	}
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if s.Model == "" {
		add("summarize.model is empty but summarize.enabled is true")
	}
	if _, err := s.APIKey.Resolve(); err != nil {
		add("summarize.api_key: %v", err)
	}
	if s.MaxCharsPerSession <= 0 {
		add("summarize.max_chars_per_session must be positive, got %d", s.MaxCharsPerSession)
	}
	if s.MaxCharsPerToolInput <= 0 {
		add("summarize.max_chars_per_tool_input must be positive, got %d", s.MaxCharsPerToolInput)
	}
	if s.MaxCharsPerToolOutput <= 0 {
		add("summarize.max_chars_per_tool_output must be positive, got %d", s.MaxCharsPerToolOutput)
	}
	if s.Concurrency <= 0 {
		add("summarize.concurrency must be positive, got %d", s.Concurrency)
	}
	if s.MaxRetries < 0 {
		add("summarize.max_retries cannot be negative, got %d", s.MaxRetries)
	}
	if s.Timeout <= 0 {
		add("summarize.timeout must be positive, got %s", s.Timeout)
	}
	switch s.DataCollection {
	case "allow", "deny":
	default:
		add("summarize.data_collection must be allow or deny, got %q", s.DataCollection)
	}
	// Empty is valid and means "send nothing, take the model's default". The rest are the
	// gateway's effort values; a model that does not support the one given gets it remapped
	// to its nearest, so this only has to reject values the gateway itself would.
	switch s.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		add("summarize.reasoning_effort must be one of none, minimal, low, medium, high, xhigh, max "+
			"(or empty for the model's own default), got %q", s.ReasoningEffort)
	}
	return errs
}
