package main

import (
	"context"
	"strings"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/summary"
)

func TestProgress_AnnouncesEverySessionAndThinsTheCallCount(t *testing.T) {
	var b strings.Builder
	p := progress(&b, 30)

	// A fan-out's worth of bare calls, then the session that they belonged to.
	for i := 1; i <= 12; i++ {
		p(summary.Event{Calls: i})
	}
	p(summary.Event{Calls: 12, Session: "the session"})

	out := b.String()

	// Every session lands, whatever the call count happens to be.
	if !strings.Contains(out, "[12/30] the session — summarised") {
		t.Fatalf("session completion not reported:\n%s", out)
	}
	// Calls are thinned, or a hundred-call run buries its own session lines.
	if !strings.Contains(out, "[10/30] calls") {
		t.Fatalf("periodic call line missing:\n%s", out)
	}
	if strings.Contains(out, "[11/30] calls") {
		t.Fatalf("every call was printed, not every %dth:\n%s", progressEvery, out)
	}
}

func TestProgress_ReportsAFailedSessionWithItsReason(t *testing.T) {
	var b strings.Builder
	p := progress(&b, 5)
	p(summary.Event{Calls: 3, Session: "broken one", Err: errTest})

	if got := b.String(); !strings.Contains(got, "broken one — failed: boom") {
		t.Fatalf("failure not reported with its reason:\n%s", got)
	}
}

// The live line and the report have to agree. Calling the same event a failure on stderr and
// an interruption in the markdown is how an operator concludes the tool broke.
func TestProgress_CallsAnInterruptionAnInterruption(t *testing.T) {
	var b strings.Builder
	p := progress(&b, 5)
	p(summary.Event{Calls: 3, Session: "stopped one", Err: context.Canceled})

	got := b.String()
	if !strings.Contains(got, "stopped one — interrupted") {
		t.Fatalf("interruption not reported as such:\n%s", got)
	}
	if strings.Contains(got, "failed") || strings.Contains(got, "context canceled") {
		t.Fatalf("interruption was reported as a failure:\n%s", got)
	}
}

// Line-based output is the point: a redrawn counter turns into a wall of control characters
// when stderr is redirected to a file, which is how -all runs are actually captured.
func TestProgress_WritesWholeLinesWithNoCarriageReturns(t *testing.T) {
	var b strings.Builder
	p := progress(&b, 10)
	for i := 1; i <= 10; i++ {
		p(summary.Event{Calls: i})
	}
	p(summary.Event{Calls: 10, Session: "done"})

	out := b.String()
	if strings.Contains(out, "\r") {
		t.Fatalf("progress used carriage returns, which mangle a redirected log:\n%q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("progress left a partial line:\n%q", out)
	}
}

var errTest = errBoom{}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
