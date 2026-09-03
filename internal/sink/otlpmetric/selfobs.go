// Sink's self-observability instruments (issue #8): this service's own operational
// state, not a GenAI operation, so - per the issue's own instruction - none of these
// carry the gen_ai.* base attribute set every instrument in record.go carries. Most
// carry no attributes at all; the few that do (current-file offset, per-sink
// rejections/pending) are documented at each instrument below with exactly why that
// one dimension earns its place, per the issue's "keep attributes minimal and justify
// what you do attach".
//
// These share the turn-derived MeterProvider (newSink's mp) and are therefore
// delivered over the SAME OTLP pipeline as every metric in record.go, per the issue's
// explicit requirement - there is no second exporter, no second collection interval,
// nothing here that could silently stop shipping while turn metrics keep flowing.
//
// Built lazily via RegisterSelfObs rather than unconditionally in newInstruments: the
// tail.Watcher and the sink tree these read from do not exist yet at Sink
// construction time (cmd/codexlb2otel builds the sinks before it builds the watcher -
// see sinks.go and main.go's run()), and every existing test in sink_test.go
// constructs a Sink without ever calling RegisterSelfObs - so none of them gained a
// new attribute key to make room for, and TestEveryEmittedAttributeKeyIsOnContract's
// walk of every data point this package emits is untouched by this file.
package otlpmetric

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/rknightion/codexlb2otel/internal/attr"
	"github.com/rknightion/codexlb2otel/internal/enrich"
	"github.com/rknightion/codexlb2otel/internal/selfobs"
)

// Self-observability attribute keys. Deliberately NOT in internal/attr's registry:
// that registry is keyed by a Field.Of func(*turn.Turn) string, and none of these
// describe a Turn at all - they describe a FILE or a SINK. attr.go stays frozen and
// Turn-only; these three live here, next to the only instruments that use them.
const (
	selfObsFile   = "codexlb.selfobs.file"
	selfObsSink   = "codexlb.selfobs.sink"
	selfObsReason = "codexlb.selfobs.reason"
)

// selfInstruments holds every self-observability instrument, built once by
// RegisterSelfObs.
type selfInstruments struct {
	ingestLag         otelmetric.Float64ObservableGauge
	filesWatched      otelmetric.Int64ObservableGauge
	currentFileOffset otelmetric.Int64ObservableGauge
	bytesRead         otelmetric.Int64ObservableCounter
	membersDecoded    otelmetric.Int64ObservableCounter
	linesDecoded      otelmetric.Int64ObservableCounter
	undecodableLines  otelmetric.Int64ObservableCounter
	partialMembers    otelmetric.Int64ObservableCounter
	decodeErrors      otelmetric.Int64ObservableCounter
	fileReplacements  otelmetric.Int64ObservableCounter
	filesReclaimed    otelmetric.Int64ObservableCounter
	turnsEmitted      otelmetric.Int64ObservableCounter
	turnsEvicted      otelmetric.Int64ObservableCounter
	openResponses     otelmetric.Int64ObservableGauge
	reducerSeries     otelmetric.Int64ObservableGauge
	reducerThreads    otelmetric.Int64ObservableGauge
	sinkRejections    otelmetric.Int64ObservableCounter
	sinkPending       otelmetric.Int64ObservableGauge
	enrichLookups     otelmetric.Int64Counter
	enrichDuration    otelmetric.Float64Histogram
	enrichCache       otelmetric.Int64ObservableGauge
	driftFindings     otelmetric.Int64ObservableGauge
}

// newSelfInstruments creates every self-observability instrument up front, the same
// eager-construction discipline newInstruments uses for the turn-derived ones (see
// its own doc comment): a name typo becomes a startup error here instead of a silent
// no-op the first time a rare path - safety buffering's own counter is the precedent
// cited there - finally executes in production.
func newSelfInstruments(meter otelmetric.Meter) (selfInstruments, error) {
	var errs []error
	must := func(name string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}

	var i selfInstruments
	var err error

	i.ingestLag, err = meter.Float64ObservableGauge(attr.MetricSelfIngestLag,
		otelmetric.WithDescription("Wall-clock now minus the newest record timestamp the "+
			"watcher has seen. The single most important self-observability signal this "+
			"service has: a stopped watcher, a stalled checkpoint, or a sink swallowing "+
			"everything all show up here first."),
		otelmetric.WithUnit("s"))
	must(attr.MetricSelfIngestLag, err)

	i.filesWatched, err = meter.Int64ObservableGauge(attr.MetricSelfFilesWatched,
		otelmetric.WithDescription("Archive files currently matched by the watcher's glob."),
		otelmetric.WithUnit("{file}"))
	must(attr.MetricSelfFilesWatched, err)

	// codexlb.selfobs.file is bounded by construction, not by a Guard cap: this gauge
	// reports at most ONE data point at a time (the live file), and the live file
	// rotates hourly, per codex-lb's own naming scheme (tail.Config's own doc
	// comment). A per-historical-file gauge was deliberately rejected - see this
	// feature's own delivery notes - because tail.Checkpoint never removes an entry
	// once seen (only marks it Deleted), so that shape grows one series per hour for
	// the life of the deployment with no upper bound.
	i.currentFileOffset, err = meter.Int64ObservableGauge(attr.MetricSelfCurrentFileOffset,
		otelmetric.WithDescription("Checkpoint offset into the archive file currently being "+
			"tailed, tagged with which file that is. Only the live file - never a "+
			"per-historical-file series, which tail.Checkpoint's own design (offsets are "+
			"marked Deleted, never removed) would grow without bound."),
		otelmetric.WithUnit("By"))
	must(attr.MetricSelfCurrentFileOffset, err)

	i.bytesRead, err = meter.Int64ObservableCounter(attr.MetricSelfBytesRead,
		otelmetric.WithDescription("Archive bytes decoded since process start."),
		otelmetric.WithUnit("By"))
	must(attr.MetricSelfBytesRead, err)

	i.membersDecoded, err = meter.Int64ObservableCounter(attr.MetricSelfMembersDecoded,
		otelmetric.WithDescription("Complete gzip members decoded since process start."),
		otelmetric.WithUnit("{member}"))
	must(attr.MetricSelfMembersDecoded, err)

	i.linesDecoded, err = meter.Int64ObservableCounter(attr.MetricSelfLinesDecoded,
		otelmetric.WithDescription("JSONL lines (frame.Record values) decoded since process "+
			"start, including ones that produced no turn."),
		otelmetric.WithUnit("{line}"))
	must(attr.MetricSelfLinesDecoded, err)

	i.undecodableLines, err = meter.Int64ObservableCounter(attr.MetricSelfUndecodableLines,
		otelmetric.WithDescription("Lines that failed to parse as a frame.Record or whose "+
			"event payload the reducer could not decode. The offset still advances past "+
			"these - retrying an undecodable line would loop forever - so this counter is "+
			"the only record that they existed."),
		otelmetric.WithUnit("{line}"))
	must(attr.MetricSelfUndecodableLines, err)

	i.partialMembers, err = meter.Int64ObservableCounter(attr.MetricSelfPartialMemberReads,
		otelmetric.WithDescription("Decode passes that found a gzip member cut off mid-write "+
			"at the tail of the buffer - the NORMAL steady state of tailing a file codex-lb "+
			"is still appending to, not an error. Deliberately a separate counter from "+
			"codexlb.selfobs.decode_errors so the two are never conflated on a dashboard."),
		otelmetric.WithUnit("{read}"))
	must(attr.MetricSelfPartialMemberReads, err)

	i.decodeErrors, err = meter.Int64ObservableCounter(attr.MetricSelfDecodeErrors,
		otelmetric.WithDescription("Complete-but-corrupt gzip members - genuine archive "+
			"corruption, never a partial trailing member (see "+
			"codexlb.selfobs.partial_member_reads for that normal case)."),
		otelmetric.WithUnit("{error}"))
	must(attr.MetricSelfDecodeErrors, err)

	i.fileReplacements, err = meter.Int64ObservableCounter(attr.MetricSelfFileReplacements,
		otelmetric.WithDescription("Archive files detected as replaced at the same path - "+
			"codex-lb recreating the same hourly filename as an unrelated file."),
		otelmetric.WithUnit("{file}"))
	must(attr.MetricSelfFileReplacements, err)

	i.filesReclaimed, err = meter.Int64ObservableCounter(attr.MetricSelfFilesReclaimed,
		otelmetric.WithDescription("Fully-ingested archive files deleted under the "+
			"operator's opt-in retention rules (archive.retain_days, archive.delete_after). "+
			"Flat while retention is on means deletion is failing, not that nothing "+
			"was due - a read-only archive mount returns EROFS on every attempt."),
		otelmetric.WithUnit("{file}"))
	must(attr.MetricSelfFilesReclaimed, err)

	i.turnsEmitted, err = meter.Int64ObservableCounter(attr.MetricSelfTurnsEmitted,
		otelmetric.WithDescription("Turns handed to the sink tree since process start, "+
			"including ones forced closed by eviction."),
		otelmetric.WithUnit("{turn}"))
	must(attr.MetricSelfTurnsEmitted, err)

	i.turnsEvicted, err = meter.Int64ObservableCounter(attr.MetricSelfTurnsEvicted,
		otelmetric.WithDescription("Turns forced closed because no response.completed ever "+
			"arrived, measured against the archive's own newest-record watermark - never "+
			"against wall-clock time (see codexlb.selfobs.ingest_lag_seconds' own doc "+
			"comment for why those are two different clocks kept deliberately apart)."),
		otelmetric.WithUnit("{turn}"))
	must(attr.MetricSelfTurnsEvicted, err)

	i.openResponses, err = meter.Int64ObservableGauge(attr.MetricSelfOpenResponses,
		otelmetric.WithDescription("Responses still streaming, mid-turn. A steadily growing "+
			"value means responses are never completing - this is what makes that leak "+
			"visible before it becomes an outage."),
		otelmetric.WithUnit("{response}"))
	must(attr.MetricSelfOpenResponses, err)

	i.reducerSeries, err = meter.Int64ObservableGauge(attr.MetricSelfReducerSeries,
		otelmetric.WithDescription("Distinct cumulative-baseline series (turn.State.Prev) "+
			"the reducer is tracking as of the last checkpoint save. Persists across a "+
			"restart and is never pruned, so unbounded growth here is a leak in the "+
			"persisted half of the reducer's state."),
		otelmetric.WithUnit("{series}"))
	must(attr.MetricSelfReducerSeries, err)

	i.reducerThreads, err = meter.Int64ObservableGauge(attr.MetricSelfReducerThreads,
		otelmetric.WithDescription("Distinct threads (turn.State.Seq) the reducer holds a "+
			"turn-sequence counter for as of the last checkpoint save. Same leak shape as "+
			"codexlb.selfobs.reducer_series, tracked separately since the two maps can grow "+
			"independently."),
		otelmetric.WithUnit("{thread}"))
	must(attr.MetricSelfReducerThreads, err)

	// codexlb.selfobs.sink and .reason are both bounded by construction: sink is one
	// of a handful of Sink.Name() values fixed at compile time (loki, otlpmetric,
	// otlptrace, agento11y), and reason is one of sink.Reason* - Loki's own
	// documented rejection classes plus the two this service adds (too_old,
	// rejected). Neither needs a Guard cap; both are closed sets this process itself
	// defines.
	i.sinkRejections, err = meter.Int64ObservableCounter(attr.MetricSelfSinkRejections,
		otelmetric.WithDescription("Deliveries a sink refused, by sink and by reason - "+
			"exactly sink.Rejection, reused rather than duplicated. Every reason means a "+
			"record that will never exist at its destination."),
		otelmetric.WithUnit("{record}"))
	must(attr.MetricSelfSinkRejections, err)

	i.sinkPending, err = meter.Int64ObservableGauge(attr.MetricSelfSinkPending,
		otelmetric.WithDescription("Records a sink is holding but has not yet durably "+
			"delivered (sink.Reporter.Pending()), by sink."),
		otelmetric.WithUnit("{record}"))
	must(attr.MetricSelfSinkPending, err)

	i.enrichLookups, err = meter.Int64Counter(attr.MetricSelfEnrichLookups,
		otelmetric.WithDescription("Postgres enrichment attempts by bounded result."),
		otelmetric.WithUnit("{lookup}"))
	must(attr.MetricSelfEnrichLookups, err)

	i.enrichDuration, err = meter.Float64Histogram(attr.MetricSelfEnrichLookupDuration,
		otelmetric.WithDescription("Duration of point lookups attempted against Postgres."),
		otelmetric.WithUnit("s"))
	must(attr.MetricSelfEnrichLookupDuration, err)

	i.enrichCache, err = meter.Int64ObservableGauge(attr.MetricSelfEnrichCacheEntries,
		otelmetric.WithDescription("Response rows currently retained in the enrichment LRU."),
		otelmetric.WithUnit("{entry}"))
	must(attr.MetricSelfEnrichCacheEntries, err)

	i.driftFindings, err = meter.Int64ObservableGauge(attr.MetricArchiveDriftFindings,
		otelmetric.WithDescription("Archive profile differences from the embedded baseline by severity."),
		otelmetric.WithUnit("{finding}"))
	must(attr.MetricArchiveDriftFindings, err)

	if len(errs) > 0 {
		return selfInstruments{}, errors.Join(errs...)
	}
	return i, nil
}

// RegisterSelfObs wires collect as the source of this process's own operational
// metrics and registers ONE callback that reads a single, consistent
// selfobs.Snapshot per collection tick - every instrument above observes from that
// same Snapshot value, not eighteen independent reads each racing the watcher's own
// poll loop separately.
//
// Must be called at most once; a second call returns an error rather than silently
// registering a duplicate callback (which would double-report every value here).
// cmd/codexlb2otel calls this after both the tail.Watcher and the sink tree exist,
// which is after this Sink is constructed - see New's own doc comment for why that
// ordering means this cannot happen inside newInstruments.
func (s *Sink) RegisterSelfObs(collect func() selfobs.Snapshot) error {
	if s.selfObsRegistered {
		return fmt.Errorf("otlpmetric: RegisterSelfObs already called once for this Sink")
	}
	meter := s.mp.Meter(meterScope)

	inst, err := newSelfInstruments(meter)
	if err != nil {
		return fmt.Errorf("otlpmetric: building self-observability instruments: %w", err)
	}

	observables := []otelmetric.Observable{
		inst.ingestLag, inst.filesWatched, inst.currentFileOffset, inst.bytesRead,
		inst.membersDecoded, inst.linesDecoded, inst.undecodableLines, inst.partialMembers,
		inst.decodeErrors, inst.fileReplacements, inst.filesReclaimed, inst.turnsEmitted,
		inst.turnsEvicted, inst.openResponses, inst.reducerSeries, inst.reducerThreads,
		inst.sinkRejections, inst.sinkPending, inst.enrichCache, inst.driftFindings,
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o otelmetric.Observer) error {
		snap := collect()

		if lag, ok := snap.IngestLagSeconds(); ok {
			o.ObserveFloat64(inst.ingestLag, lag)
		}
		o.ObserveInt64(inst.filesWatched, snap.FilesWatched)
		if snap.CurrentFile != "" {
			o.ObserveInt64(inst.currentFileOffset, snap.CurrentFileOffset,
				otelmetric.WithAttributes(attribute.String(selfObsFile, snap.CurrentFile)))
		}
		o.ObserveInt64(inst.bytesRead, snap.BytesRead)
		o.ObserveInt64(inst.membersDecoded, snap.MembersRead)
		o.ObserveInt64(inst.linesDecoded, snap.LinesDecoded)
		o.ObserveInt64(inst.undecodableLines, snap.UndecodableLines)
		o.ObserveInt64(inst.partialMembers, snap.PartialMemberReads)
		o.ObserveInt64(inst.decodeErrors, snap.DecodeErrors)
		o.ObserveInt64(inst.fileReplacements, snap.FileReplacements)
		o.ObserveInt64(inst.filesReclaimed, snap.FilesReclaimed)
		o.ObserveInt64(inst.turnsEmitted, snap.TurnsEmitted)
		o.ObserveInt64(inst.turnsEvicted, snap.TurnsEvicted)
		o.ObserveInt64(inst.openResponses, snap.OpenResponses)
		o.ObserveInt64(inst.reducerSeries, snap.ReducerSeriesCount)
		o.ObserveInt64(inst.reducerThreads, snap.ReducerThreadCount)
		o.ObserveInt64(inst.enrichCache, snap.EnrichCacheEntries)
		if snap.HasDrift {
			o.ObserveInt64(inst.driftFindings, snap.DriftBreaking,
				otelmetric.WithAttributes(attribute.String(attr.SelfObsSeverity, "breaking")))
			o.ObserveInt64(inst.driftFindings, snap.DriftNew,
				otelmetric.WithAttributes(attribute.String(attr.SelfObsSeverity, "new")))
			o.ObserveInt64(inst.driftFindings, snap.DriftInfo,
				otelmetric.WithAttributes(attribute.String(attr.SelfObsSeverity, "info")))
		}

		for _, sh := range snap.Sinks {
			sinkAttr := attribute.String(selfObsSink, sh.Name)
			o.ObserveInt64(inst.sinkPending, int64(sh.Pending), otelmetric.WithAttributes(sinkAttr))
			for _, rej := range sh.Rejections {
				if rej.Count <= 0 {
					continue
				}
				o.ObserveInt64(inst.sinkRejections, rej.Count, otelmetric.WithAttributes(
					sinkAttr, attribute.String(selfObsReason, rej.Reason)))
			}
		}
		return nil
	}, observables...)
	if err != nil {
		return fmt.Errorf("otlpmetric: registering self-observability callback: %w", err)
	}
	s.selfObsRegistered = true
	s.selfInst = inst
	return nil
}

// RecordEnrichment records one lookup result on the same OTLP pipeline as the turn
// metrics. Lookup latency is recorded only when a database query was attempted;
// cache hits and disabled enrichment therefore do not manufacture a zero duration.
func (s *Sink) RecordEnrichment(ctx context.Context, result enrich.Result) {
	if s == nil || !s.selfObsRegistered || result.Outcome == "" {
		return
	}
	s.selfInst.enrichLookups.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String(attr.SelfObsResult, string(result.Outcome))))
	switch result.Outcome {
	case enrich.OutcomeDBHit, enrich.OutcomeMiss, enrich.OutcomeError:
		s.selfInst.enrichDuration.Record(ctx, result.LookupDuration.Seconds())
	}
}
