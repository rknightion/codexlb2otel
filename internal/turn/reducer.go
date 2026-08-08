package turn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rknightion/codexlb2otel/internal/frame"
)

// Reducer accumulates frames into Turns. It is not safe for concurrent use.
//
// State is per-request_id while a response streams, plus per-thread cumulative
// counters used to convert the server's logical-turn metrics into per-response
// deltas. Both must survive a process restart or the first turn after one produces
// a bogus delta - see Snapshot/Restore.
type Reducer struct {
	open map[string]*Turn      // request_id -> in-flight turn
	prev map[string]cumulative // series key -> last cumulative snapshot
	seq  map[string]int        // thread_id -> logical turn counter
	// lastSeen guards against diffing a new reading against a baseline old enough
	// that unobserved traffic has advanced it. Keyed like prev. See applyTiming.
	lastSeen map[string]time.Time
	seen     *seenSet // content already emitted, bounded
	opts     Options
}

// Options tunes content capture. DefaultOptions is what New uses.
type Options struct {
	// MaxToolOutputChars truncates captured tool output. These carry whole command
	// results and are the largest content source in the archive; the untruncated
	// length is always recorded regardless. Zero disables tool-output capture.
	MaxToolOutputChars int
	// MaxPromptChars truncates captured prompts. Zero disables prompt capture.
	MaxPromptChars int
	// SeenSetSize bounds the dedup set for re-sent conversation history.
	SeenSetSize int
	// MaxThreadGap is how long a thread's cumulative baseline stays valid. Past it,
	// unobserved traffic is assumed and the baseline is reset rather than diffed
	// against - see applyTiming.
	MaxThreadGap time.Duration
}

// DefaultOptions captures full prompts and generously truncated tool output.
func DefaultOptions() Options {
	return Options{
		MaxToolOutputChars: 4096,
		MaxPromptChars:     32768,
		SeenSetSize:        32768,
		MaxThreadGap:       30 * time.Minute,
	}
}

// New returns an empty Reducer using DefaultOptions.
func New() *Reducer { return NewWithOptions(DefaultOptions()) }

// NewWithOptions returns an empty Reducer with explicit content-capture settings.
func NewWithOptions(o Options) *Reducer {
	if o.SeenSetSize <= 0 {
		o.SeenSetSize = DefaultOptions().SeenSetSize
	}
	if o.MaxThreadGap <= 0 {
		o.MaxThreadGap = DefaultOptions().MaxThreadGap
	}
	return &Reducer{
		open:     map[string]*Turn{},
		prev:     map[string]cumulative{},
		seq:      map[string]int{},
		lastSeen: map[string]time.Time{},
		seen:     newSeenSet(o.SeenSetSize),
		opts:     o,
	}
}

// Add feeds one frame in. It returns a completed Turn when the frame closed one,
// otherwise nil. A frame whose payload is unparseable is counted and ignored.
func (r *Reducer) Add(rec *frame.Record) (*Turn, error) {
	// Frames with no request_id cannot be correlated, and were observed carrying
	// connection-scoped errors. Keying them all on "" would merge unrelated frames
	// into one ever-growing turn, so they are handled standalone and never stored.
	if rec.RequestID == "" {
		return r.addUncorrelated(rec)
	}

	t := r.open[rec.RequestID]
	if t == nil {
		t = &Turn{
			RequestID:      rec.RequestID,
			SessionID:      rec.Header(frame.HdrSession),
			ThreadID:       rec.Header(frame.HdrThread),
			ParentThreadID: rec.Header(frame.HdrParentThread),
			InstallationID: rec.Header(frame.HdrInstallation),
			WindowID:       rec.Header(frame.HdrWindow),
			AccountID:      rec.AccountID,
			Originator:     rec.Header(frame.HdrOriginator),
			ClientVersion:  rec.Header(frame.HdrVersion),
			Family:         rec.Family(),
			IsSubagent:     rec.IsSubagent(),
			SubagentKind:   rec.Header(frame.HdrSubagent),
			FirstTS:        rec.Timestamp,
			ItemCounts:     map[string]int{},
		}
		t.TraceID, t.SpanID = rec.Trace()
		r.open[rec.RequestID] = t
	}
	t.LastTS = rec.Timestamp
	t.Frames++
	t.Bytes += len(rec.Payload.Text)

	if rec.IsTransport() {
		applyTransport(t, rec)
	}

	ev, ok := rec.ParseEvent()
	if !ok {
		return nil, nil
	}

	// Deltas dominate volume; count and discard without decoding.
	if frame.IsDelta(ev.Type) {
		switch ev.Type {
		case frame.EvOutputTextDelta:
			t.TextDeltas++
		default:
			t.ToolDeltas++
		}
		return nil, nil
	}

	switch ev.Type {
	case frame.EvResponseCreate:
		r.applyCreate(t, ev)
	case frame.EvRateLimits:
		r.applyRateLimits(t, ev)
	case frame.EvResponseCreated:
		r.applyCreated(t, ev)
	case frame.EvOutputItemDone:
		r.applyOutputItem(t, ev)
	case frame.EvWebsocketTiming:
		r.applyTiming(t, ev)

	// An error is terminal: no response.completed will follow, so the turn is closed
	// here or it would linger until evicted and be reported as merely incomplete,
	// losing the reason. Upstream overload and the websocket connection cap both
	// arrive this way.
	case frame.EvError:
		r.applyError(t, ev)
		r.ensureLogicalTurn(t)
		delete(r.open, rec.RequestID)
		return t, nil

	case frame.EvResponseCompleted:
		if err := r.applyCompleted(t, ev); err != nil {
			return nil, err
		}
		r.ensureLogicalTurn(t)
		delete(r.open, rec.RequestID)
		return t, nil
	}
	return nil, nil
}

// applyTransport records a websocket-level error or close.
//
// These carry no protocol event: their payload is a plain-text reason such as
// "received 1012 (service restart); then sent 1012", so ParseEvent rejects it and a
// payload-only decoder sees nothing at all. The close code lives in the archive's
// extra block rather than in the payload.
func applyTransport(t *Turn, rec *frame.Record) {
	if rec.Extra.CloseCode != nil {
		t.CloseCode = rec.Extra.CloseCode
	}
	if rec.Extra.FrameType == frame.FrameError {
		t.FrameErrors++
	}
	if t.TransportEvent == "" {
		t.TransportEvent = rec.Payload.Text
	}
}

// addUncorrelated handles a frame with no request_id.
//
// Keying these on "" would merge unrelated frames from every connection into one
// ever-growing turn. Only frames that mean something standalone are kept: protocol
// errors, and the websocket-level close and error frames - which are precisely the
// ones that report a connection dying, and which arrive with no request_id far more
// often than not (13 of 20 in the captured corpus).
func (r *Reducer) addUncorrelated(rec *frame.Record) (*Turn, error) {
	ev, evOK := rec.ParseEvent()
	isProtocolError := evOK && ev.Type == frame.EvError
	if !isProtocolError && !rec.IsTransport() {
		return nil, nil
	}
	t := &Turn{
		SessionID:      rec.Header(frame.HdrSession),
		ThreadID:       rec.Header(frame.HdrThread),
		ParentThreadID: rec.Header(frame.HdrParentThread),
		InstallationID: rec.Header(frame.HdrInstallation),
		AccountID:      rec.AccountID,
		Originator:     rec.Header(frame.HdrOriginator),
		Family:         rec.Family(),
		IsSubagent:     rec.IsSubagent(),
		FirstTS:        rec.Timestamp,
		LastTS:         rec.Timestamp,
		Frames:         1,
		Bytes:          len(rec.Payload.Text),
		ItemCounts:     map[string]int{},
	}
	t.TraceID, t.SpanID = rec.Trace()
	if isProtocolError {
		r.applyError(t, ev)
	} else {
		applyTransport(t, rec)
		t.Status = StatusTransport
	}
	r.ensureLogicalTurn(t)
	return t, nil
}

type errorEvent struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Param   string `json:"param"`
	} `json:"error"`
	Status int `json:"status"`
}

func (r *Reducer) applyError(t *Turn, ev frame.Event) {
	t.Status = StatusError
	var e errorEvent
	if ev.Decode(&e) != nil {
		return
	}
	t.ErrorType = e.Error.Type
	t.ErrorCode = e.Error.Code
	t.ErrorMessage = e.Error.Message
}

// ensureLogicalTurn assigns an id to a response that never reported timing metrics.
// Prewarm responses do no engine work and carry no cumulative series, so each is its
// own logical turn - there is nothing for it to be a continuation of.
func (r *Reducer) ensureLogicalTurn(t *Turn) {
	if t.TurnID != "" {
		// The server told us which turn this is. Trust it over anything inferred: the
		// inference exists only because this field is absent on prewarms and on the
		// HTTP family's error frames.
		t.LogicalTurnID = t.TurnID
		return
	}
	if t.LogicalTurnID != "" {
		return
	}
	r.seq[t.ThreadID]++
	t.LogicalTurnSeq = r.seq[t.ThreadID]
	t.LogicalTurnID = fmt.Sprintf("%s:%d", t.ThreadID, t.LogicalTurnSeq)
}

type createEvent struct {
	Model     string `json:"model"`
	Reasoning struct {
		Effort  string `json:"effort"`
		Context string `json:"context"`
		Mode    string `json:"mode"`
	} `json:"reasoning"`
	Text struct {
		Verbosity string `json:"verbosity"`
	} `json:"text"`
	// ServiceTier is the tier the client ASKS for, and is top-level on response.create
	// rather than nested under a response object the way the server's own reading is.
	// See Turn.ServiceTierRequested for why the two are kept apart.
	ServiceTier    string         `json:"service_tier"`
	Instructions   string         `json:"instructions"`
	PrevResponseID string         `json:"previous_response_id"`
	PromptCacheKey string         `json:"prompt_cache_key"`
	ParallelTools  bool           `json:"parallel_tool_calls"`
	Input          []inputItem    `json:"input"`
	ClientMetadata clientMetadata `json:"client_metadata"`
}

// clientMetadata is the per-response identity block. Everything here is authoritative
// for THIS response, unlike the same-named request headers, which are fixed when the
// websocket opens and go stale immediately.
type clientMetadata struct {
	SessionID string `json:"session_id"`
	ThreadID  string `json:"thread_id"`
	TurnID    string `json:"turn_id"`
	// TurnMetadata is a JSON document embedded as a string.
	TurnMetadata string `json:"x-codex-turn-metadata"`
	// StreamStartMs is the client's own request start, as a decimal string.
	StreamStartMs string `json:"x-codex-ws-stream-request-start-ms"`
	Lite          string `json:"ws_request_header_x_openai_internal_codex_responses_lite"`
	Subagent      string `json:"x-openai-subagent"`
	ParentThread  string `json:"x-codex-parent-thread-id"`
}

// inputItem is one entry of the conversation history the client re-sends on every
// turn.
//
// Both content and output are polymorphic - a plain JSON string on some items and a
// list of typed parts on others. They are held raw and flattened leniently because a
// strict type here is a live hazard: Go's json decoder aborts the WHOLE event on one
// mismatched field, so a single unexpected shape silently discarded every prompt,
// the model and the input count for that entire turn. That happened.
type inputItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	CallID  string          `json:"call_id"`
	Output  json.RawMessage `json:"output"`
	Content json.RawMessage `json:"content"`
	// Author and Recipient are task paths, present on agent_message items.
	Author    string `json:"author"`
	Recipient string `json:"recipient"`
	// Tools is populated only on an "additional_tools" item - measured as the ONLY
	// place the wire protocol carries a tool catalogue (#22 item 1 assumed a
	// top-level response.create field named tools[]; the corpus has none across
	// 8,916 response.create events). See Turn.Tools.
	Tools json.RawMessage `json:"tools"`
}

// wireTool is one entry of an additional_tools item's own tools[] array. Deliberately
// shallow, same reasoning as ToolDef: parameters/output_schema/nested tools[] are
// client-authored JSON Schema of unbounded shape and are left undecoded on purpose.
type wireTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Strict      bool   `json:"strict"`
}

// flattenText renders a polymorphic text field: a bare JSON string, or a list of
// parts each carrying a text field. Anything else yields "" rather than an error.
func flattenText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// imageParts counts the image parts of a polymorphic content field and fingerprints
// them WITHOUT carrying any pixels.
//
// Images arrive inline as {"type":"input_image","image_url":"data:image/png;base64,..."}
// - measured at 784 KB for a single screenshot on the 2026-08-07 corpus. That payload
// is the one thing that must never reach a Loki line, and not for tidiness: Loki
// discards an over-long line WHOLE rather than truncating it, so one pasted screenshot
// would take the user's message with it. flattenText already drops these, because an
// image part has no text field - but it dropped them so completely that an image-only
// message vanished with no trace that an image was ever sent. The count is the signal.
//
// The MIME type is the one thing worth recovering from the payload, and it is free:
// a data URI declares it in the first 30 bytes, before the base64 begins. Without it a
// consumer knows only "an image was here", which is barely worth emitting; with it the
// sigil Media part and the Loki line can say what kind. It is read from the URI's own
// declaration rather than sniffed from the bytes, so it is bounded by construction.
//
// The fingerprint exists only to keep the dedup key distinct between different images,
// and is deliberately bounded to a prefix and a length: the same image is re-sent in
// the conversation history on every later turn of the thread, so hashing the whole
// payload each time would cost more than the fact is worth.
func imageParts(raw json.RawMessage) (n int, mime, fingerprint string) {
	if len(raw) == 0 {
		return 0, "", ""
	}
	// image_url is held raw rather than as a string: it is a bare data URI today, but
	// the same field is an object elsewhere in OpenAI's API, and a typed string here
	// would fail the WHOLE array decode on that shape and silently count zero images.
	var parts []struct {
		Type     string          `json:"type"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return 0, "", ""
	}
	var fp strings.Builder
	for _, p := range parts {
		if len(p.ImageURL) == 0 {
			continue
		}
		n++
		head := string(p.ImageURL)
		if len(head) > 48 {
			head = head[:48]
		}
		if mime == "" {
			mime = dataURIMediaType(head)
		}
		fmt.Fprintf(&fp, "%s:%d;", head, len(p.ImageURL))
	}
	return n, mime, fp.String()
}

// dataURIMediaType reads the media type out of the head of a data URI, tolerating
// anything that is not one.
//
// s is a JSON-quoted prefix, so the leading quote is expected: `"data:image/png;base64,`.
// Only the declared type is returned and nothing is inferred from the payload - a URI
// with no media type, or a plain https:// link, yields "" rather than a guess.
func dataURIMediaType(s string) string {
	const prefix = `"data:`
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return ""
	}
	// The media type runs to the first ";" (parameters, including ";base64") or ","
	// (the payload). Whichever comes first ends it.
	if i := strings.IndexAny(rest, ";,"); i >= 0 {
		return rest[:i]
	}
	return ""
}

func (r *Reducer) applyCreate(t *Turn, ev frame.Event) {
	var c createEvent
	if ev.Decode(&c) != nil {
		return
	}
	if t.Model == "" {
		t.Model = c.Model
	}
	t.Effort = c.Reasoning.Effort
	t.ReasoningCtx = c.Reasoning.Context
	t.ReasoningMode = c.Reasoning.Mode
	t.Verbosity = c.Text.Verbosity
	t.ServiceTierRequested = c.ServiceTier
	t.InputItems = len(c.Input)
	t.PrevResponseID = c.PrevResponseID
	t.PromptCacheKey = c.PromptCacheKey
	t.ParallelTools = c.ParallelTools
	r.applyClientMetadata(t, c.ClientMetadata)

	// The system prompt arrives as a top-level field, not an input item. It runs to
	// 67 KB but takes only a handful of distinct values, so the body is emitted once
	// globally and every response carries just the hash and length.
	if c.Instructions != "" {
		t.InstructionsChars = len(c.Instructions)
		t.InstructionsHash = shortHash(c.Instructions)
		if r.opts.MaxPromptChars > 0 && r.seen.add("instructions", t.InstructionsHash) {
			text, _ := truncate(c.Instructions, r.opts.MaxPromptChars)
			t.Prompts = append(t.Prompts, Prompt{
				Role: "instructions", Chars: len(c.Instructions), Text: text,
			})
		}
	}
	r.captureInput(t, c.Input)
}

// applyClientMetadata takes identity from the per-response block.
//
// The turn metadata is deliberately read from HERE rather than from the request
// header of the same name. The header is written once when the websocket opens and
// never refreshed: over one hour of live traffic it reported request_kind="prewarm"
// with an empty turn_id for 177 of 199 genuine turns. Reading the header would
// mislabel 89% of turns.
func (r *Reducer) applyClientMetadata(t *Turn, cm clientMetadata) {
	if cm.ThreadID != "" {
		t.ThreadID = cm.ThreadID
	}
	if cm.SessionID != "" {
		t.SessionID = cm.SessionID
	}
	if cm.TurnID != "" {
		t.TurnID = cm.TurnID
	}
	if cm.ParentThread != "" {
		t.ParentThreadID = cm.ParentThread
		t.IsSubagent = true
	}
	if cm.Subagent != "" {
		t.SubagentKind = cm.Subagent
	}
	t.Lite = cm.Lite == "true"
	if ms, err := strconv.ParseInt(cm.StreamStartMs, 10, 64); err == nil && ms > 0 {
		t.ClientRequestStart = time.UnixMilli(ms).UTC()
	}

	m, ok := frame.ParseTurnMeta(cm.TurnMetadata)
	if !ok {
		return
	}
	if m.TurnID != "" {
		t.TurnID = m.TurnID
	}
	t.RequestKind = m.RequestKind
	t.ThreadSource = m.ThreadSource
	t.Sandbox = m.Sandbox
	t.ForkedFromThreadID = m.ForkedFromThreadID
	if m.SubagentKind != "" {
		t.SubagentKind = m.SubagentKind
	}
	if m.WindowID != "" {
		t.WindowID = m.WindowID
	}
	if m.InstallationID != "" {
		t.InstallationID = m.InstallationID
	}
	if m.ParentThreadID != "" {
		t.ParentThreadID = m.ParentThreadID
		t.IsSubagent = true
	}
	if m.ParentTurnID != "" {
		t.ParentTurnID = m.ParentTurnID
	}
	if ts, ok := m.TurnStart(); ok {
		t.TurnStart = ts
	}
}

// shortHash identifies a large near-static body without shipping it.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// captureInput recovers the input half of the conversation - user prompts and tool
// results - which appears nowhere else in the archive.
//
// response.create re-sends the whole history every turn (arrays of 800+ items were
// routine), so each item is emitted only on the first turn that carries it.
func (r *Reducer) captureInput(t *Turn, items []inputItem) {
	for _, it := range items {
		switch it.Type {
		case "message":
			if r.opts.MaxPromptChars <= 0 {
				continue
			}
			body := flattenText(it.Content)
			images, imageMIME, imageFP := imageParts(it.Content)
			if body == "" && images == 0 {
				continue
			}
			// Developer messages are harness boilerplate, identical across every
			// thread, and the largest prompts by far - deduplicate them globally so
			// a 36 KB system preamble is stored once rather than once per thread.
			// User and assistant messages are the actual conversation and are scoped
			// per thread so each thread reconstructs in full.
			scope := t.ThreadID
			if it.Role == "developer" {
				scope = "global"
			}
			// The fingerprint is part of the key so two image-only messages, which both
			// flatten to an empty body, do not dedup to each other.
			if !r.seen.add(scope, "msg", it.Role, body, imageFP) {
				continue
			}
			text, _ := truncate(body, r.opts.MaxPromptChars)
			t.Prompts = append(t.Prompts, Prompt{
				Role: it.Role, Chars: len(body), Text: text,
				Images: images, ImageMIME: imageMIME,
			})
			t.InputImages += images

		// agent_message is one agent talking to another, addressed by task path. It is
		// the only record of the multi-agent topology anywhere in the capture, so it is
		// kept even when prompt capture is off - the edge matters more than the body.
		case "agent_message":
			body := flattenText(it.Content)
			if !r.seen.add(t.ThreadID, "agentmsg", it.Author, it.Recipient, body) {
				continue
			}
			am := AgentMessage{Author: it.Author, Recipient: it.Recipient, Chars: len(body)}
			if r.opts.MaxPromptChars > 0 {
				am.Text, _ = truncate(body, r.opts.MaxPromptChars)
			}
			t.AgentMessages = append(t.AgentMessages, am)

		case "custom_tool_call_output", "function_call_output":
			if r.opts.MaxToolOutputChars <= 0 {
				continue
			}
			body := flattenText(it.Output)
			if body == "" {
				body = flattenText(it.Content)
			}
			if body == "" {
				continue
			}
			// Dedupe on call id where present: the same output is re-sent verbatim on
			// every subsequent turn of the thread.
			key := it.CallID
			if key == "" {
				key = body
			}
			if !r.seen.add(t.ThreadID, "out", key) {
				continue
			}
			text, cut := truncate(body, r.opts.MaxToolOutputChars)
			t.ToolOutputs = append(t.ToolOutputs, ToolOutput{
				CallID: it.CallID, Chars: len(body), Truncated: cut, Text: text,
			})

		// additional_tools is the ONLY input item type observed carrying a tool
		// catalogue - see Turn.Tools and wireTool. Kept unconditional on content-capture
		// options, same as agent_message: the catalogue's presence/hash matters even
		// with body capture off.
		case "additional_tools":
			r.applyToolCatalogue(t, it.Tools)
		}
	}
}

// applyToolCatalogue decodes an additional_tools item's tools[] and gets the SAME
// dedup-by-hash treatment as InstructionsHash, mirrored deliberately rather than
// invented fresh: ToolsHash is set on every response that carries a catalogue,
// Tools (the bodies) only on the first response globally to carry that exact hash.
// Scope is global, like instructions, not per-thread like Prompts - matching
// InstructionsHash exactly rather than the message-scoping pattern, per instruction.
func (r *Reducer) applyToolCatalogue(t *Turn, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var tools []wireTool
	if json.Unmarshal(raw, &tools) != nil {
		return
	}
	t.ToolsHash = shortHash(string(raw))
	if !r.seen.add("tools", t.ToolsHash) {
		return
	}
	t.Tools = make([]ToolDef, 0, len(tools))
	for _, tl := range tools {
		t.Tools = append(t.Tools, ToolDef{
			Name: tl.Name, Kind: tl.Type, Description: tl.Description, Strict: tl.Strict,
		})
	}
}

// truncate cuts s to at most max bytes without splitting a UTF-8 rune, reporting
// whether it cut.
func truncate(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// rateLimitWindow is one reported quota window.
type rateLimitWindow struct {
	UsedPercent       float64 `json:"used_percent"`
	WindowMinutes     int     `json:"window_minutes"`
	ResetAfterSeconds float64 `json:"reset_after_seconds"`
}

// rateLimitBlock is the shape used by both the account's own limits and each entry of
// additional_rate_limits. Every level is a pointer: the server sends explicit nulls
// for windows a plan does not have.
type rateLimitBlock struct {
	Allowed      bool             `json:"allowed"`
	LimitReached bool             `json:"limit_reached"`
	Primary      *rateLimitWindow `json:"primary"`
	Secondary    *rateLimitWindow `json:"secondary"`
}

type rateLimitEvent struct {
	PlanType   string                     `json:"plan_type"`
	RateLimits *rateLimitBlock            `json:"rate_limits"`
	Additional map[string]*rateLimitBlock `json:"additional_rate_limits"`
	Credits    *struct {
		HasCredits bool   `json:"has_credits"`
		Unlimited  bool   `json:"unlimited"`
		Balance    string `json:"balance"`
	} `json:"credits"`
}

// applyRateLimits records this account's quota headroom.
//
// codex-lb balances across several ChatGPT accounts, so these readings are only
// meaningful grouped by AccountID - aggregated across accounts they average away
// exactly the exhaustion the load balancer exists to avoid.
func (r *Reducer) applyRateLimits(t *Turn, ev frame.Event) {
	var e rateLimitEvent
	if ev.Decode(&e) != nil {
		return
	}
	t.PlanType = e.PlanType
	if c := e.Credits; c != nil {
		t.HasCredits = c.HasCredits
		t.CreditsUnlimited = c.Unlimited
		t.CreditsBalance = c.Balance
	}
	if rl := e.RateLimits; rl != nil {
		t.RateLimitReached = rl.LimitReached
		t.RateLimitAllowed = rl.Allowed
		if p := rl.Primary; p != nil {
			t.RateLimitUsedPercent = p.UsedPercent
			t.RateLimitWindowMin = p.WindowMinutes
			t.RateLimitResetSeconds = p.ResetAfterSeconds
		}
		if s := rl.Secondary; s != nil {
			t.RateLimit2UsedPercent = s.UsedPercent
			t.RateLimit2WindowMin = s.WindowMinutes
		}
	}
	// Keyed by model name and bounded by the models on the plan, so this is safe to
	// fan out into per-model gauges.
	for model, blk := range e.Additional {
		if blk == nil || blk.Primary == nil {
			continue
		}
		if t.ExtraRateLimits == nil {
			t.ExtraRateLimits = map[string]float64{}
		}
		t.ExtraRateLimits[model] = blk.Primary.UsedPercent
	}
}

type createdEvent struct {
	Response struct {
		ID          string `json:"id"`
		Model       string `json:"model"`
		ServiceTier string `json:"service_tier"`
		SafetyID    string `json:"safety_identifier"`
	} `json:"response"`
}

func (r *Reducer) applyCreated(t *Turn, ev frame.Event) {
	var e createdEvent
	if ev.Decode(&e) != nil {
		return
	}
	t.ResponseID = e.Response.ID
	t.ServiceTier = e.Response.ServiceTier
	t.SafetyID = e.Response.SafetyID
	if t.Model == "" {
		t.Model = e.Response.Model
	}
}

type outputItemEvent struct {
	Item struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Status    string `json:"status"`
		Input     string `json:"input"`
		Arguments string `json:"arguments"`
		Phase     string `json:"phase"`
		Encrypted string `json:"encrypted_content"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`
}

// spawnArgs is the subset of spawn_agent arguments worth keeping. The `message`
// field is an OpenAI-encrypted blob and is deliberately dropped.
type spawnArgs struct {
	TaskName string `json:"task_name"`
	Model    string `json:"model"`
	Effort   string `json:"reasoning_effort"`
}

func (r *Reducer) applyOutputItem(t *Turn, ev frame.Event) {
	var e outputItemEvent
	if ev.Decode(&e) != nil {
		return
	}
	it := e.Item
	t.ItemCounts[it.Type]++

	switch it.Type {
	case "custom_tool_call":
		t.ToolCalls = append(t.ToolCalls, ToolCall{
			Kind: "custom", Name: it.Name, CallID: it.CallID,
			Status: it.Status, InputChars: len(it.Input), Input: it.Input,
		})
	case "function_call":
		tc := ToolCall{
			Kind: "function", Name: it.Name, CallID: it.CallID,
			Status: it.Status, InputChars: len(it.Arguments),
		}
		var a spawnArgs
		if json.Unmarshal([]byte(it.Arguments), &a) == nil {
			tc.TaskName, tc.SubModel, tc.SubEffort = a.TaskName, a.Model, a.Effort
		}
		t.ToolCalls = append(t.ToolCalls, tc)
	case "message":
		var body string
		for _, c := range it.Content {
			if c.Type == "output_text" {
				body += c.Text
			}
		}
		t.Messages = append(t.Messages, Message{Phase: it.Phase, Chars: len(body), Text: body})
	case "reasoning":
		// Content is Fernet-encrypted by OpenAI and cannot be read. Size only.
		t.ReasoningEnc += len(it.Encrypted)
	}
}

type timingEvent struct {
	M struct {
		TotalTurnTimeS     *float64 `json:"total_turn_time_s"`
		PreInferenceMs     *float64 `json:"pre_inference_ms"`
		FirstTTFTMs        *float64 `json:"first_sampled_message_ttft_ms"`
		FirstReasoning     *int     `json:"first_sampled_message_reasoning_tokens"`
		EngineQueueMaxMs   *float64 `json:"engine_queue_max_ms"`
		NumEngineCalls     *int     `json:"num_engine_calls"`
		EngineIDs          *string  `json:"engine_ids"`
		PromptTokensTotal  *int     `json:"engine_total_prompt_tokens_total"`
		CachedTokensTotal  *int     `json:"engine_cached_prompt_tokens_total"`
		SampledTokensTotal *int     `json:"num_sampled_tokens_total"`
		ClientToolPauseMs  *float64 `json:"client_tool_pause_total_ms"`

		// A second timing domain reported alongside engine_iapi_*. Divergence between
		// the two is what tells you whether time went in the engine or around it.
		ServiceTotalMs *float64 `json:"engine_service_total_ms"`
		ServiceTTFTMs  *float64 `json:"engine_service_ttft_total_ms"`
		IapiTTFTMs     *float64 `json:"engine_iapi_ttft_total_ms"`

		TextDeltaCount *int     `json:"websocket_output_text_delta_count"`
		TextDeltaTBTMs *float64 `json:"websocket_output_text_delta_tbt_ms"`
		PauseCount     *int     `json:"responses_duration_pause_count"`

		// The nine cumulative fields #12's corpus comment identified (measured 2026-08-07
		// against all 16 archives): same logical_turn scope, same turn-boundary reset
		// rule as NumEngineCalls/PromptTokensTotal/etc above, so they are diffed the
		// identical way in applyTiming. EngineUncachedPromptTokensTotal is
		// PromptTokensTotal's uncached sibling - the only one of the pair that was
		// unread; PromptTokensTotal itself has decoded into EnginePromptTokensDelta
		// since before this change.
		ServiceInferenceMs           *float64 `json:"engine_service_inference_total_ms"`
		ServiceSamplingMs            *float64 `json:"engine_service_sampling_total_ms"`
		IapiInferenceMs              *float64 `json:"engine_iapi_inference_total_ms"`
		IapiSamplingMs               *float64 `json:"engine_iapi_sampling_total_ms"`
		ExclEngineAndToolMs          *float64 `json:"responses_duration_excl_engine_and_client_tool_time_ms"`
		ExclEngineWaitSamplingMs     *float64 `json:"responses_duration_excl_engine_wait_and_sampling_ms"`
		ExclEngineWaitSamplingIapiMs *float64 `json:"responses_duration_excl_engine_wait_and_sampling_iapi_ms"`
		ExclClientToolsMs            *float64 `json:"responsesapi_duration_excl_client_tools_ms"`
		UncachedPromptTokensTotal    *int     `json:"engine_uncached_prompt_tokens_total"`

		// The three TBT fields are running AVERAGES across engine calls, not sums -
		// EngineServiceMinusIapiTBTMs measured going negative (min -6.70) in the
		// corpus, which a cumulative sum cannot do. Used directly, per response, never
		// diffed.
		ServiceTBTMs          *float64 `json:"engine_service_tbt_across_engine_calls_ms"`
		IapiTBTMs             *float64 `json:"engine_iapi_tbt_across_engine_calls_ms"`
		ServiceMinusIapiTBTMs *float64 `json:"engine_service_minus_iapi_tbt_across_engine_calls_ms"`

		// ToolCallDurationsMs is cumulative across the logical turn like the fields
		// above, but is an ARRAY (one entry appended per tool call) rather than a
		// running total, so applyTiming suffix-diffs it instead of subtracting. Present
		// on every logical_turn timing event in the corpus (8,883/8,883), length 1-100,
		// never null. ToolCallDurationsTruncated is its companion flag and DROPS THE
		// "_ms" its sibling carries - a naming trap that already cost one round trip.
		// null on 8,853/8,883 events, true on the rest, never false, so it decodes
		// straight into a per-response bool rather than needing diff treatment either.
		ToolCallDurationsMs        *[]float64 `json:"responsesapi_duration_excl_engine_per_tool_call_ms"`
		ToolCallDurationsTruncated *bool      `json:"responsesapi_duration_excl_engine_per_tool_call_truncated"`

		// TimingScope is "logical_turn" for the block above - which is exactly why the
		// fields above need delta conversion and CriticalPath does not.
		TimingScope  string `json:"timing_scope"`
		CriticalPath *struct {
			Scope             string   `json:"scope"`
			Coverage          string   `json:"coverage"`
			BoundaryType      string   `json:"boundary_type"`
			NumEngineCalls    *int     `json:"num_engine_calls"`
			EngineWallMs      *float64 `json:"engine_wall_ms"`
			EngineIapiTotalMs *float64 `json:"engine_iapi_total_ms"`
			HarnessUnblocked  *float64 `json:"responses_harness_unblocked_ms"`
			PreInferenceMs    *float64 `json:"responses_pre_inference_ms"`
			SamplingStreamMs  *float64 `json:"sampling_and_stream_total_ms"`
			ClientToolPauseMs *float64 `json:"client_tool_pause_ms"`
			OtherMs           *float64 `json:"responses_other_ms"`
		} `json:"critical_path"`
	} `json:"timing_metrics"`
}

// applyCriticalPath copies the server's own per-response breakdown across.
//
// This is measured, not derived: the block declares scope="response" while its parent
// declares scope="logical_turn". Where Coverage is not "complete" the server is saying
// it could not find a harness boundary, so consumers should exclude those rather than
// average them in.
func applyCriticalPath(t *Turn, e *timingEvent) {
	cp := e.M.CriticalPath
	if cp == nil {
		return
	}
	t.CriticalPath = CriticalPath{Coverage: cp.Coverage, BoundaryType: cp.BoundaryType}
	c := &t.CriticalPath
	if cp.NumEngineCalls != nil {
		c.EngineCalls = *cp.NumEngineCalls
	}
	for _, f := range []struct {
		src *float64
		dst *float64
	}{
		{cp.EngineWallMs, &c.EngineWallMs},
		{cp.EngineIapiTotalMs, &c.EngineIapiMs},
		{cp.HarnessUnblocked, &c.HarnessUnblockedMs},
		{cp.PreInferenceMs, &c.PreInferenceMs},
		{cp.SamplingStreamMs, &c.SamplingStreamMs},
		{cp.ClientToolPauseMs, &c.ClientToolPauseMs},
		{cp.OtherMs, &c.OtherMs},
	} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
}

// seriesKey identifies the cumulative counter series a response belongs to.
//
// NOT the thread alone, which is what this was until 2026-08-07 and which was wrong.
// A thread runs more than one logical-turn series AT THE SAME TIME: the platform fires
// memory-consolidation responses (request_kind "memory") concurrently with the user's
// turn, on the same thread_id, and each keeps its own counters starting from
// num_engine_calls=1. Prewarm and compaction are separate series for the same reason.
//
// Diffing them against one shared baseline attributed a whole series to a single
// response of the other. Measured on the 2026-08-07 corpus, thread 019fdba7 at
// 11:06:04: the turn series moved 731,429 -> 916,716 engine prompt tokens, a true
// delta of 185,287, but was diffed against the interleaved memory series' 121,470 and
// reported 795,246 - 4.3x too high. Twelve of 8,078 turns were affected, and the only
// visible symptom was a negative sampled-token delta on those same twelve, because
// sampled tokens happen to be the one counter where the memory series runs AHEAD of
// the turn series. The token over-count, which is the damage, was silent.
func seriesKey(t *Turn) string { return t.ThreadID + "\x00" + t.RequestKind }

// applyTiming converts the cumulative logical-turn counters into per-response
// deltas. This is the single most important correctness property in the package:
// the raw values accumulate across a turn and reset at the next one, so summing
// them directly overcounts by roughly 5.7x.
func (r *Reducer) applyTiming(t *Turn, ev frame.Event) {
	var e timingEvent
	if ev.Decode(&e) != nil {
		return
	}
	m := e.M

	if m.EngineIDs != nil {
		t.EngineIDs = *m.EngineIDs
	}
	if m.PreInferenceMs != nil {
		t.PreInferenceMs = *m.PreInferenceMs
	}
	if m.FirstTTFTMs != nil {
		t.TTFTMs = *m.FirstTTFTMs
	}
	if m.EngineQueueMaxMs != nil {
		t.EngineQueueMaxMs = *m.EngineQueueMaxMs
	}
	if m.FirstTTFTMs != nil {
		t.FirstMsgTTFTMs = *m.FirstTTFTMs
	}
	if m.FirstReasoning != nil {
		t.FirstMsgReasoning = *m.FirstReasoning
	}
	if m.ServiceTotalMs != nil {
		t.EngineServiceTotal = *m.ServiceTotalMs
	}
	if m.ServiceTTFTMs != nil {
		t.EngineServiceTTFT = *m.ServiceTTFTMs
	}
	if m.IapiTTFTMs != nil {
		t.EngineIapiTTFT = *m.IapiTTFTMs
	}
	if m.TextDeltaCount != nil {
		t.TextDeltaCount = *m.TextDeltaCount
	}
	if m.TextDeltaTBTMs != nil {
		t.TextDeltaTBTMs = *m.TextDeltaTBTMs
	}
	if m.PauseCount != nil {
		t.PauseCount = *m.PauseCount
	}
	// Running averages across engine calls, not sums - see the struct doc on why
	// these are never delta-treated.
	if m.ServiceTBTMs != nil {
		t.EngineServiceTBTMs = *m.ServiceTBTMs
	}
	if m.IapiTBTMs != nil {
		t.EngineIapiTBTMs = *m.IapiTBTMs
	}
	if m.ServiceMinusIapiTBTMs != nil {
		t.EngineServiceMinusIapiTBTMs = *m.ServiceMinusIapiTBTMs
	}
	if m.ToolCallDurationsTruncated != nil {
		t.ToolCallDurationsTruncated = *m.ToolCallDurationsTruncated
	}
	applyCriticalPath(t, &e)

	cur := cumulative{}
	if m.NumEngineCalls != nil {
		cur.engineCalls = *m.NumEngineCalls
	}
	if m.TotalTurnTimeS != nil {
		cur.turnTimeS = *m.TotalTurnTimeS
	}
	if m.SampledTokensTotal != nil {
		cur.sampledTokens = *m.SampledTokensTotal
	}
	if m.PromptTokensTotal != nil {
		cur.promptTokens = *m.PromptTokensTotal
	}
	if m.CachedTokensTotal != nil {
		cur.cachedTokens = *m.CachedTokensTotal
	}
	if m.ClientToolPauseMs != nil {
		cur.toolPauseMs = *m.ClientToolPauseMs
	}
	if m.ServiceInferenceMs != nil {
		cur.serviceInferenceMs = *m.ServiceInferenceMs
	}
	if m.ServiceSamplingMs != nil {
		cur.serviceSamplingMs = *m.ServiceSamplingMs
	}
	if m.IapiInferenceMs != nil {
		cur.iapiInferenceMs = *m.IapiInferenceMs
	}
	if m.IapiSamplingMs != nil {
		cur.iapiSamplingMs = *m.IapiSamplingMs
	}
	if m.ExclEngineAndToolMs != nil {
		cur.exclEngineAndToolMs = *m.ExclEngineAndToolMs
	}
	if m.ExclEngineWaitSamplingMs != nil {
		cur.exclEngineWaitSamplingMs = *m.ExclEngineWaitSamplingMs
	}
	if m.ExclEngineWaitSamplingIapiMs != nil {
		cur.exclEngineWaitSamplingIapiMs = *m.ExclEngineWaitSamplingIapiMs
	}
	if m.ExclClientToolsMs != nil {
		cur.exclClientToolsMs = *m.ExclClientToolsMs
	}
	if m.UncachedPromptTokensTotal != nil {
		cur.uncachedPromptTokens = *m.UncachedPromptTokensTotal
	}

	key := seriesKey(t)
	prev, seen := r.prev[key]

	// A stale baseline is worse than none. If this series was last seen long enough
	// ago that the intervening traffic cannot have been observed - the service was
	// down, or the archive has a gap - the counters have advanced unseen and diffing
	// against the old baseline attributes all of that missing work to this one
	// response. Treat a gap as a fresh turn instead: the cost is one under-counted
	// response rather than an arbitrarily large over-count.
	if seen && !t.FirstTS.IsZero() {
		if last, ok := r.lastSeen[key]; ok && t.FirstTS.Sub(last) > r.opts.MaxThreadGap {
			seen = false
		}
	}

	// A new logical turn begins whenever the cumulative engine-call counter fails to
	// advance. Validated across live traffic: zero negative deltas.
	if !seen || m.NumEngineCalls == nil || cur.engineCalls <= prev.engineCalls {
		r.seq[t.ThreadID]++
		// Starting a turn with no prior baseline means this response absorbs all the
		// work the turn had already done before we started watching. Deltas here are
		// upper bounds, not measurements, so they are flagged: consumers can exclude
		// them rather than silently believing an over-count. Only happens on a cold
		// start, after a gap, or genuinely at a turn boundary - and at a real
		// boundary the counters restart anyway, so the flag is only meaningful when
		// no baseline existed.
		t.BaselineReset = !seen
		prev = cumulative{}
	}
	t.LogicalTurnSeq = r.seq[t.ThreadID]
	t.LogicalTurnID = fmt.Sprintf("%s:%d", t.ThreadID, t.LogicalTurnSeq)
	r.ensureLogicalTurn(t) // server turn id wins where it exists

	t.EngineCallsDelta = cur.engineCalls - prev.engineCalls
	t.TurnTimeSecondsDelta = cur.turnTimeS - prev.turnTimeS
	t.SampledTokensDelta = cur.sampledTokens - prev.sampledTokens
	t.EnginePromptTokensDelta = cur.promptTokens - prev.promptTokens
	t.EngineCachedTokensDelta = cur.cachedTokens - prev.cachedTokens
	t.ClientToolPauseMsDelta = cur.toolPauseMs - prev.toolPauseMs
	t.EngineServiceInferenceMsDelta = cur.serviceInferenceMs - prev.serviceInferenceMs
	t.EngineServiceSamplingMsDelta = cur.serviceSamplingMs - prev.serviceSamplingMs
	t.EngineIapiInferenceMsDelta = cur.iapiInferenceMs - prev.iapiInferenceMs
	t.EngineIapiSamplingMsDelta = cur.iapiSamplingMs - prev.iapiSamplingMs
	t.ResponsesExclEngineAndToolMsDelta = cur.exclEngineAndToolMs - prev.exclEngineAndToolMs
	t.ResponsesExclEngineWaitSamplingMsDelta = cur.exclEngineWaitSamplingMs - prev.exclEngineWaitSamplingMs
	t.ResponsesExclEngineWaitSamplingIapiMsDelta = cur.exclEngineWaitSamplingIapiMs - prev.exclEngineWaitSamplingIapiMs
	t.ResponsesAPIExclClientToolsMsDelta = cur.exclClientToolsMs - prev.exclClientToolsMs
	t.EngineUncachedPromptTokensDelta = cur.uncachedPromptTokens - prev.uncachedPromptTokens

	// The per-tool-call array is cumulative like the fields above but cannot be
	// diffed by subtraction - it is a growing list, not a running total. base is
	// prev.toolCallDurationsMs, which the reset branch above already zeroed to nil
	// when this response starts a new series, so a cold start or turn boundary
	// naturally reports the WHOLE array as newly seen without special-casing it here.
	//
	// The len(newArr) < len(base) check is an independent safety net on top of that:
	// #12's corpus measurement found the array shrinks in exactly the responses where
	// the engine-calls reset above also fires, but that was an empirical finding
	// about this corpus, not a protocol guarantee, so a shrink is treated as its own
	// reset signal regardless of what the engine-calls counter says.
	if m.ToolCallDurationsMs != nil {
		newArr := *m.ToolCallDurationsMs
		base := prev.toolCallDurationsMs
		if len(newArr) < len(base) {
			base = nil
		}
		if len(newArr) > len(base) {
			t.ToolCallDurationsMs = append([]float64(nil), newArr[len(base):]...)
		}
		cur.toolCallDurationsMs = newArr
	} else {
		// Not present on this tick - carry the last known array forward so the next
		// response that DOES carry it diffs against real history, not a phantom
		// empty baseline. Turn.ToolCallDurationsMs stays nil: absent, not zero.
		cur.toolCallDurationsMs = prev.toolCallDurationsMs
	}

	// Only advance the baseline once the server actually reported engine work;
	// prewarm responses report no counters and must not reset it.
	if m.NumEngineCalls != nil {
		r.prev[key] = cur
		r.lastSeen[key] = t.LastTS
	}
}

type completedEvent struct {
	// SafetyBuffering is polymorphic: literal false on nearly every response, an
	// object on the rare one where the platform re-ran it through another model. A
	// non-pointer struct here would abort the decode of the WHOLE event on the common
	// `false` case and silently drop every usage figure with it.
	SafetyBuffering json.RawMessage `json:"safety_buffering"`
	Response        struct {
		Status         string  `json:"status"`
		Model          string  `json:"model"`
		ServiceTier    string  `json:"service_tier"`
		SafetyID       string  `json:"safety_identifier"`
		CreatedAt      float64 `json:"created_at"`
		CompletedAt    float64 `json:"completed_at"`
		CacheRetention string  `json:"prompt_cache_retention"`
		ParallelTools  bool    `json:"parallel_tool_calls"`
		// Temperature and TopP are pointers so a genuinely absent value (never observed
		// in the 2026-08-07 corpus, but response.max_output_tokens shows the wire does
		// send explicit nulls for request parameters) is not confused with a real 0.0 -
		// a valid, if unusual, temperature. #18 called both absent; measured present on
		// all 8,883 response.completed events in the corpus (constant at 1.0 / 0.98 in
		// that sample - a property of this traffic, not a wire guarantee).
		Temperature *float64 `json:"temperature"`
		TopP        *float64 `json:"top_p"`
		Reasoning   struct {
			Context string `json:"context"`
			Mode    string `json:"mode"`
		} `json:"reasoning"`
		ToolUsage struct {
			WebSearch struct {
				NumRequests int `json:"num_requests"`
			} `json:"web_search"`
			ImageGen struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"image_gen"`
		} `json:"tool_usage"`
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

func (r *Reducer) applyCompleted(t *Turn, ev frame.Event) error {
	var e completedEvent
	if err := ev.Decode(&e); err != nil {
		return fmt.Errorf("turn: decode response.completed: %w", err)
	}
	t.Status = e.Response.Status
	if e.Response.Model != "" {
		t.Model = e.Response.Model
	}
	if e.Response.ServiceTier != "" {
		t.ServiceTier = e.Response.ServiceTier
	}
	if e.Response.SafetyID != "" {
		t.SafetyID = e.Response.SafetyID
	}
	if e.Response.Reasoning.Context != "" {
		t.ReasoningCtx = e.Response.Reasoning.Context
	}
	if e.Response.Reasoning.Mode != "" {
		t.ReasoningMode = e.Response.Reasoning.Mode
	}
	t.CacheRetention = e.Response.CacheRetention
	t.ParallelTools = t.ParallelTools || e.Response.ParallelTools
	if e.Response.Temperature != nil {
		t.Temperature = *e.Response.Temperature
	}
	if e.Response.TopP != nil {
		t.TopP = *e.Response.TopP
	}
	t.ServerCreatedAt = epoch(e.Response.CreatedAt)
	t.ServerCompletedAt = epoch(e.Response.CompletedAt)
	t.WebSearchRequests = e.Response.ToolUsage.WebSearch.NumRequests
	t.ImageGenTokens = e.Response.ToolUsage.ImageGen.TotalTokens
	applySafetyBuffering(t, e.SafetyBuffering)
	if u := e.Response.Usage; u != nil {
		t.InputTokens = u.InputTokens
		t.OutputTokens = u.OutputTokens
		t.TotalTokens = u.TotalTokens
		t.CachedTokens = u.InputTokensDetails.CachedTokens
		t.CacheWriteTokens = u.InputTokensDetails.CacheWriteTokens
		t.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
		if u.InputTokens > 0 {
			t.CacheHitRatio = float64(u.InputTokensDetails.CachedTokens) / float64(u.InputTokens)
		}
	}
	return nil
}

// epoch converts a fractional unix timestamp, tolerating an absent zero.
func epoch(secs float64) time.Time {
	if secs <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(secs * 1000)).UTC()
}

// applySafetyBuffering decodes the polymorphic safety_buffering field. It is `false`
// on almost every response and an object on the few where the platform re-ran the
// request through a different model.
func applySafetyBuffering(t *Turn, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		t.SafetyBuffering = b
		return
	}
	var o struct {
		RetryModel string   `json:"retry_model"`
		UseCases   []string `json:"use_cases"`
		Reasons    []string `json:"reasons"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return
	}
	t.SafetyBuffering = true
	t.SafetyRetryModel = o.RetryModel
	t.SafetyUseCases = o.UseCases
	t.SafetyReasons = o.Reasons
}

// Open reports how many responses are still streaming. A steadily growing value
// means responses are never completing, which is worth alerting on.
func (r *Reducer) Open() int { return len(r.open) }
