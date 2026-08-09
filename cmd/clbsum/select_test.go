package main

import (
	"strings"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/live"
)

func rows(n int) []*live.Thread {
	out := make([]*live.Thread, n)
	for i := range out {
		out[i] = &live.Thread{ThreadID: string(rune('a' + i))}
	}
	return out
}

// -pick takes the same 1-based numbers -list prints. Off-by-one here silently summarises
// the wrong sessions, which no amount of reading the output would reveal.
func TestByNumber(t *testing.T) {
	r := rows(5)

	got, err := byNumber(r, "1,3,5")
	if err != nil {
		t.Fatalf("byNumber: %v", err)
	}
	want := []string{"a", "c", "e"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ThreadID != id {
			t.Errorf("row %d = %s, want %s", i, got[i].ThreadID, id)
		}
	}
}

func TestByNumber_ToleratesSpacingAndRepeats(t *testing.T) {
	got, err := byNumber(rows(5), " 2 , 2,4, ")
	if err != nil {
		t.Fatalf("byNumber: %v", err)
	}
	if len(got) != 2 || got[0].ThreadID != "b" || got[1].ThreadID != "d" {
		t.Errorf("got %v, want b and d once each", ids(got))
	}
}

func TestByNumber_RejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"out of range high": "9",
		"zero is not a row": "0",
		"negative":          "-1",
		"not a number":      "three",
		"nothing at all":    ",,",
	}
	for name, pick := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := byNumber(rows(5), pick); err == nil {
				t.Errorf("byNumber(%q) was accepted, want an error", pick)
			}
		})
	}
}

// The out-of-range message has to say how many there are, or the fix is guesswork.
func TestByNumber_OutOfRangeSaysHowMany(t *testing.T) {
	_, err := byNumber(rows(3), "7")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "there are 3 sessions") {
		t.Errorf("error = %q, want it to say how many sessions there are", err)
	}
}

func ids(ts []*live.Thread) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ThreadID
	}
	return out
}
