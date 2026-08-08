package live

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestHeadlineOf(t *testing.T) {
	cases := []struct {
		name    string
		turn    turn.Turn
		content bool
		want    string
	}{
		{
			name:    "newest user prompt wins",
			turn:    turn.Turn{Prompts: []turn.Prompt{{Role: "user", Text: "first ask"}, {Role: "user", Text: "second ask"}}},
			content: true,
			want:    "second ask",
		},
		{
			// Developer prompts are system scaffolding, identical across every thread, so
			// they distinguish nothing when a real request is available.
			name: "user beats developer regardless of order",
			turn: turn.Turn{Prompts: []turn.Prompt{
				{Role: "user", Text: "what I asked"},
				{Role: "developer", Text: "you are a helpful assistant"},
			}},
			content: true,
			want:    "what I asked",
		},
		{
			name:    "developer used only when there is no user prompt",
			turn:    turn.Turn{Prompts: []turn.Prompt{{Role: "developer", Text: "system scaffolding"}}},
			content: true,
			want:    "system scaffolding",
		},
		{
			// A spawn_agent call names the CHILD and appears on the SPAWNER's turn, so
			// letting it win made a parent thread announce itself as its child's task and
			// hide the human request that drove it. It is an activity of the parent, not
			// the parent's identity.
			name: "a spawn call never becomes the spawning thread's own headline",
			turn: turn.Turn{
				ToolCalls: []turn.ToolCall{{Name: "spawn_agent", TaskName: "/root/lab_baseline"}},
				Prompts:   []turn.Prompt{{Role: "user", Text: "the request that actually drove this"}},
			},
			content: true,
			want:    "the request that actually drove this",
		},
		{
			name:    "no prompt text escapes when content is disabled",
			turn:    turn.Turn{Prompts: []turn.Prompt{{Role: "user", Text: "the secret plan"}}},
			content: false,
			want:    "",
		},
		{
			name:    "nothing to say",
			turn:    turn.Turn{},
			content: true,
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := headlineOf(&tc.turn, tc.content); got != tc.want {
				t.Errorf("headlineOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestActivityOf(t *testing.T) {
	cases := []struct {
		name    string
		turn    turn.Turn
		content bool
		want    string
	}{
		{
			name:    "newest tool call with its argument",
			turn:    turn.Turn{ToolCalls: []turn.ToolCall{{Name: "read"}, {Name: "shell", Input: "go test ./..."}}},
			content: true,
			want:    "shell go test ./...",
		},
		{
			// Tool NAMES are bounded and structural - they are the whole point of the view.
			// The arguments are arbitrary text the model wrote.
			name:    "tool name survives content being disabled but its argument does not",
			turn:    turn.Turn{ToolCalls: []turn.ToolCall{{Name: "shell", Input: "cat /etc/passwd"}}},
			content: false,
			want:    "shell",
		},
		{
			name:    "assistant message when no tool ran",
			turn:    turn.Turn{Messages: []turn.Message{{Text: "here is the answer"}}},
			content: true,
			want:    "here is the answer",
		},
		{
			name:    "falls back to a status when there is nothing to show",
			turn:    turn.Turn{Messages: []turn.Message{{Text: "hidden"}}},
			content: false,
			want:    "responded",
		},
		{
			// The type and code are enum-like; the MESSAGE embeds ids and free text, so it
			// is content even though it reads like metadata.
			name:    "error reports type and code, never the message",
			turn:    turn.Turn{Status: turn.StatusError, ErrorType: "rate_limit", ErrorCode: "429", ErrorMessage: "resp_abc exceeded"},
			content: true,
			want:    "error rate_limit 429",
		},
		{
			name:    "incomplete",
			turn:    turn.Turn{Status: turn.StatusIncomplete},
			content: true,
			want:    "incomplete",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := activityOf(&tc.turn, tc.content); got != tc.want {
				t.Errorf("activityOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestActivityOfInFlight(t *testing.T) {
	cases := []struct {
		name    string
		f       turn.InFlight
		content bool
		want    string
	}{
		{
			// The state that only the in-flight view can ever show: a completed turn is by
			// definition no longer reasoning.
			name:    "no output at all means it is reasoning",
			f:       turn.InFlight{},
			content: true,
			want:    "thinking",
		},
		{
			name:    "text deltas with no message yet",
			f:       turn.InFlight{TextDeltas: 12},
			content: true,
			want:    "writing",
		},
		{
			name:    "a spawn in progress names the child before the child exists",
			f:       turn.InFlight{SpawnedTask: "/root/lab", LastToolCall: "spawn_agent"},
			content: true,
			want:    "spawning /root/lab",
		},
		{
			name:    "tool call",
			f:       turn.InFlight{LastToolCall: "shell", LastToolInput: "ls -la"},
			content: true,
			want:    "shell ls -la",
		},
		{
			name:    "message text is withheld without content, and the state stands in",
			f:       turn.InFlight{LastMessage: "secret", TextDeltas: 3},
			content: false,
			want:    "writing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := activityOfInFlight(tc.f, tc.content); got != tc.want {
				t.Errorf("activityOfInFlight = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFirstLine(t *testing.T) {
	t.Run("folds newlines rather than taking only the first line", func(t *testing.T) {
		// Taking literally the first line would usually yield a bare "Task:" header.
		got := firstLine("Task:\n  fix the flaky test\n\n  in package foo")
		want := "Task: fix the flaky test in package foo"
		if got != want {
			t.Errorf("firstLine = %q, want %q", got, want)
		}
	})

	t.Run("truncation is rune-counted and stays valid UTF-8", func(t *testing.T) {
		// A byte-counted cut lands mid-sequence here. json.Marshal would then silently
		// rewrite the tail to U+FFFD, so the bug surfaces as mojibake in the browser
		// rather than as an error anywhere near this code.
		got := firstLine(strings.Repeat("é", headlineChars*2))
		if !utf8.ValidString(got) {
			t.Fatalf("firstLine produced invalid UTF-8: %q", got)
		}
		if !strings.HasSuffix(got, "…") {
			t.Errorf("truncated output is not marked as truncated: %q", got)
		}
		if n := utf8.RuneCountInString(strings.TrimSuffix(got, "…")); n > headlineChars {
			t.Errorf("kept %d runes, want at most %d", n, headlineChars)
		}
	})

	t.Run("whitespace only", func(t *testing.T) {
		if got := firstLine("  \n\t "); got != "" {
			t.Errorf("firstLine = %q, want empty", got)
		}
	})
}
