package live

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestAskOf(t *testing.T) {
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
			// Developer-role messages are harness scaffolding without exception in the
			// corpus. An earlier version fell back to one when no user prompt existed, on
			// the reasoning that something beats nothing - it does not, it put a 17 KB "You
			// are Codex, an agent based on GPT-5" identically on every thread at once.
			name:    "a developer prompt is never used, even as a last resort",
			turn:    turn.Turn{Prompts: []turn.Prompt{{Role: "developer", Text: "system scaffolding"}}},
			content: true,
			want:    "",
		},
		{
			// Measured: real human turns in the corpus were 89 and 119 chars; every
			// machine-injected user-role message was 8k-42k. Nothing lands between.
			name: "an oversized user message is injected scaffolding, not a human ask",
			turn: turn.Turn{Prompts: []turn.Prompt{
				{Role: "user", Text: "the real short ask"},
				{Role: "user", Text: strings.Repeat("x", scaffoldChars+1)},
			}},
			content: true,
			want:    "the real short ask",
		},
		{
			// 426 chars - under the size threshold, genuinely the NEWEST user message on a
			// compacted thread, and identical on every thread that has one. Left in, it
			// labels precisely the long-running threads a human most needs to tell apart.
			name: "the compaction instruction is filtered despite being short and newest",
			turn: turn.Turn{Prompts: []turn.Prompt{
				{Role: "user", Text: "deploy the change to camden"},
				{Role: "user", Text: "You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary."},
			}},
			content: true,
			want:    "deploy the change to camden",
		},
		{
			name:    "harness wrapper prompts are filtered by their opening tag",
			turn:    turn.Turn{Prompts: []turn.Prompt{{Role: "user", Text: "<recommended_plugins> Here is a list of plugins that are available"}}},
			content: true,
			want:    "",
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
			if got := askOf(&tc.turn, tc.content); got != tc.want {
				t.Errorf("askOf = %q, want %q", got, tc.want)
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
			// The message is no longer activity's job - it is the row's primary line, via
			// latestOf. Activity is the mechanical state and stays a short status word, so
			// the two do not say the same thing twice on one row.
			name:    "an assistant message does not become the activity",
			turn:    turn.Turn{Messages: []turn.Message{{Text: "here is the answer"}}},
			content: true,
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
		got := oneLine("Task:\n  fix the flaky test\n\n  in package foo", askChars)
		want := "Task: fix the flaky test in package foo"
		if got != want {
			t.Errorf("oneLine = %q, want %q", got, want)
		}
	})

	t.Run("truncation is rune-counted and stays valid UTF-8", func(t *testing.T) {
		// A byte-counted cut lands mid-sequence here. json.Marshal would then silently
		// rewrite the tail to U+FFFD, so the bug surfaces as mojibake in the browser
		// rather than as an error anywhere near this code.
		got := oneLine(strings.Repeat("é", askChars*2), askChars)
		if !utf8.ValidString(got) {
			t.Fatalf("firstLine produced invalid UTF-8: %q", got)
		}
		if !strings.HasSuffix(got, "…") {
			t.Errorf("truncated output is not marked as truncated: %q", got)
		}
		if n := utf8.RuneCountInString(strings.TrimSuffix(got, "…")); n > askChars {
			t.Errorf("kept %d runes, want at most %d", n, askChars)
		}
	})

	t.Run("whitespace only", func(t *testing.T) {
		if got := oneLine("  \n\t ", askChars); got != "" {
			t.Errorf("oneLine = %q, want empty", got)
		}
	})
}

// The only human-meaningful name a subagent has, and it exists only by indirection:
// the wire keeps uuid thread ids and slash-separated task paths in disjoint spaces and
// never states the mapping. Every agent message delivered to a thread carries that
// thread's own path as `recipient`.
func TestTaskPath(t *testing.T) {
	turns := []*turn.Turn{
		{AgentMessages: []turn.AgentMessage{
			{Author: "/root", Recipient: "/root/final_integration_review_v2"},
			{Author: "/root/final_integration_review_v2/a11y_keyboard_recheck", Recipient: "/root/final_integration_review_v2"},
		}},
		{AgentMessages: []turn.AgentMessage{
			{Author: "/root", Recipient: "/root/final_integration_review_v2"},
			// A single stray delivery must not outvote the consistent majority.
			{Author: "/root", Recipient: "/root/something_else"},
		}},
	}
	if got := TaskPath(turns); got != "/root/final_integration_review_v2" {
		t.Errorf("TaskPath = %q, want the majority recipient", got)
	}
	if got := TaskName(TaskPath(turns)); got != "final_integration_review_v2" {
		t.Errorf("TaskName = %q", got)
	}
}

func TestTaskPath_IgnoresRootAndEmpty(t *testing.T) {
	// "/root" is the primary agent, not a task, and naming every root thread "root"
	// would be noise on the one row that never needs disambiguating.
	turns := []*turn.Turn{{AgentMessages: []turn.AgentMessage{
		{Author: "/root/x", Recipient: "/root"},
		{Author: "/root/y", Recipient: ""},
	}}}
	if got := TaskPath(turns); got != "" {
		t.Errorf("TaskPath = %q, want empty", got)
	}
}

// Attributed to the thread that MADE the calls, never to the threads they created:
// subagent threads are prewarmed, so their frames precede the spawn that gives them
// work and no ordering-based join is sound.
func TestSpawnedTasks_DedupedAndAttributedToTheSpawner(t *testing.T) {
	turns := []*turn.Turn{
		{ToolCalls: []turn.ToolCall{{Name: "spawn_agent", TaskName: "/root/a11y_keyboard_recheck"}}},
		{ToolCalls: []turn.ToolCall{
			{Name: "spawn_agent", TaskName: "/root/a11y_keyboard_recheck"},
			{Name: "spawn_agent", TaskName: "/root/gate_snapshot_recheck"},
			{Name: "exec"},
		}},
	}
	got := SpawnedTasks(turns)
	want := []string{"a11y_keyboard_recheck", "gate_snapshot_recheck"}
	if len(got) != len(want) {
		t.Fatalf("SpawnedTasks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SpawnedTasks = %v, want %v", got, want)
		}
	}
}
