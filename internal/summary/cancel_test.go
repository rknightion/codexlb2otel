package summary

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rknightion/codexlb2otel/internal/live"
)

var errOops = errors.New("oops")

// An interrupted run must be RECORDED as interrupted, not left to be inferred from a page of
// cancelled contexts. This is the case that produced a report reading as a broken tool when
// nothing had gone wrong but the operator pressing Ctrl-C.
func TestRun_MarksAnInterruptedRunRatherThanReportingFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	root := &live.Thread{ThreadID: "root", Name: "the session"}
	src := mapSource{"root": {{Messages: msg("WORK")}}}

	rep := Run(ctx, &ctxClient{}, src, []Session{{Thread: root}}, Options{})

	if !rep.Canceled {
		t.Fatal("Canceled = false on a run whose context was already dead")
	}
	// The roll-up must not be attempted: it would fail on the dead context, and a window
	// narrative built from whichever sessions happened to finish would understate the period.
	if rep.Window != "" || rep.WindowErr != nil {
		t.Fatalf("roll-up was attempted: window=%q err=%v", rep.Window, rep.WindowErr)
	}
}

// A run that completes normally must not be labelled interrupted, or every report would
// carry the warning and it would stop meaning anything.
func TestRun_DoesNotMarkACompletedRunAsInterrupted(t *testing.T) {
	root := &live.Thread{ThreadID: "root", Name: "the session"}
	src := mapSource{"root": {{Messages: msg("WORK")}}}

	rep := Run(context.Background(), &fakeClient{}, src, []Session{{Thread: root}}, Options{})

	if rep.Canceled {
		t.Fatal("Canceled = true on a run that finished")
	}
}

func TestRender_SaysInterruptedRatherThanNamingACancelledContext(t *testing.T) {
	rep := Report{
		From: at(0), To: at(60),
		Canceled: true,
		Sessions: []Result{
			{Name: "finished one", Summary: "shipped the picker", First: at(0), Last: at(10)},
			{Name: "stopped one", Err: context.Canceled, First: at(0), Last: at(20)},
		},
	}

	var b strings.Builder
	if err := Render(&b, rep); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	if !strings.Contains(out, "interrupted before it finished") {
		t.Fatalf("report does not say it was interrupted:\n%s", out)
	}
	if !strings.Contains(out, "Interrupted before this session was summarised.") {
		t.Fatalf("cancelled session is not marked as interrupted:\n%s", out)
	}
	// The bare error is what sends a reader looking for a fault that is not there.
	if strings.Contains(out, "context canceled") {
		t.Fatalf("report still shows a raw cancelled context:\n%s", out)
	}
	// The session that DID finish must still be reported in full.
	if !strings.Contains(out, "shipped the picker") {
		t.Fatalf("completed work was dropped from an interrupted report:\n%s", out)
	}
}

// A genuine failure must keep saying so. Treating everything as an interruption would hide
// real faults behind a reassuring message.
func TestRender_StillReportsARealFailureAsAFailure(t *testing.T) {
	rep := Report{
		From: at(0), To: at(60),
		Sessions: []Result{{Name: "broken one", Err: errOops, First: at(0), Last: at(20)}},
	}

	var b strings.Builder
	if err := Render(&b, rep); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	if !strings.Contains(out, "Could not be summarised: oops") {
		t.Fatalf("real failure was not reported:\n%s", out)
	}
	if strings.Contains(out, "Interrupted") {
		t.Fatalf("real failure was mislabelled as an interruption:\n%s", out)
	}
}

// ctxClient answers with whatever the context says, which is what a client does once the run
// has been cancelled underneath it.
type ctxClient struct{}

func (ctxClient) Complete(ctx context.Context, _, _ string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "summary", nil
}
