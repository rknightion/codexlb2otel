// This is the ONLY file in the repository that imports the OpenRouter SDK.
//
// The SDK is pre-1.0 and moving fast - 32 patch releases on v0.7 alone - so an upgrade
// that changes its types should break exactly one file, not the summariser. Everything
// else in the package, and every test, codes against summary.Client instead.
package summary

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/models/operations"
	"github.com/OpenRouterTeam/go-sdk/models/sdkerrors"
	"github.com/OpenRouterTeam/go-sdk/optionalnullable"
	"github.com/cenkalti/backoff/v5"
)

// OpenRouterOptions configures the live client.
type OpenRouterOptions struct {
	APIKey  string
	Model   string
	BaseURL string
	Timeout time.Duration
	// MaxRetries bounds the backoff on 429 and 5xx. Zero means no retry.
	MaxRetries int
	// ReasoningEffort is sent on every request. Empty sends nothing and takes the model's
	// own default.
	ReasoningEffort string
	// ResponseCache opts every request into OpenRouter's response cache, where a
	// byte-identical request is served free and never reaches a provider.
	ResponseCache bool

	// ZDR restricts routing to zero-data-retention endpoints; DataCollection is "allow"
	// or "deny". Both are sent on EVERY request rather than configured once on the
	// account, so the guarantee travels with the request and does not depend on an
	// account setting nobody can see from here.
	ZDR            bool
	DataCollection string
}

// OpenRouter is a Client backed by the OpenRouter API.
type OpenRouter struct {
	sdk  *openrouter.OpenRouter
	opts OpenRouterOptions
	// session pins every call of one run to one provider endpoint. See newSessionID.
	session string
}

// newSessionID mints the sticky-routing key for one run.
//
// Without it, OpenRouter derives the key by hashing the first system message and the first
// non-system message. Every call here shares a system prompt but carries a DIFFERENT digest
// as its first user message, so every call hashes differently and scatters across
// providers - and a prompt cache that is never landed on twice is no cache at all. With a
// session id, stickiness also starts on the first successful request rather than only once
// a cache hit has been observed.
//
// Random per run rather than constant: a fixed value would pin every run this tool ever
// makes onto one provider forever, which is load-balancing sabotage for a cache that
// expires after minutes of inactivity anyway.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Not worth failing a summarising run over. Losing the session id costs cache
		// warmth, not correctness.
		return "clbsum"
	}
	return "clbsum-" + hex.EncodeToString(b[:])
}

// NewOpenRouter builds a live client.
func NewOpenRouter(o OpenRouterOptions) (*OpenRouter, error) {
	if o.APIKey == "" {
		return nil, errors.New("openrouter: api key is empty")
	}
	if o.Model == "" {
		return nil, errors.New("openrouter: model is empty")
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.DataCollection == "" {
		o.DataCollection = string(components.DataCollectionDeny)
	}

	sdkOpts := []openrouter.SDKOption{
		openrouter.WithSecurity(o.APIKey),
		openrouter.WithTimeout(o.Timeout),
		// Identifies this tool in OpenRouter's own activity view, which is the only
		// place the operator can later audit what was sent and when.
		openrouter.WithXTitle("codexlb2otel/clbsum"),
	}
	if o.BaseURL != "" {
		sdkOpts = append(sdkOpts, openrouter.WithServerURL(o.BaseURL))
	}

	if o.ResponseCache {
		// The SDK exposes no per-request header hook, so the opt-in rides on the HTTP
		// client. Harmless when the header is unrecognised.
		sdkOpts = append(sdkOpts, openrouter.WithClient(&headerClient{
			base:    &http.Client{Timeout: o.Timeout},
			headers: map[string]string{"X-OpenRouter-Cache": "true"},
		}))
	}

	return &OpenRouter{sdk: openrouter.New(sdkOpts...), opts: o, session: newSessionID()}, nil
}

// headerClient adds fixed headers to every request the SDK makes.
type headerClient struct {
	base    *http.Client
	headers map[string]string
}

func (c *headerClient) Do(req *http.Request) (*http.Response, error) {
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	return c.base.Do(req)
}

// Complete sends one non-streaming completion and returns its text.
func (c *OpenRouter) Complete(ctx context.Context, system, user string) (string, error) {
	dc := components.DataCollection(c.opts.DataCollection)
	stream := false

	req := components.ChatRequest{
		Model:  &c.opts.Model,
		Stream: &stream,
		// Pins this run to one provider endpoint so the shared system prefix stays cached.
		SessionID: &c.session,
		Provider: optionalnullable.From(&components.ProviderPreferences{
			Zdr:            optionalnullable.From(&c.opts.ZDR),
			DataCollection: optionalnullable.From(&dc),
		}),
		Messages: []components.ChatMessages{
			components.CreateChatMessagesSystem(components.ChatSystemMessage{
				Role:    components.ChatSystemMessageRoleSystem,
				Content: components.CreateChatSystemMessageContentStr(system),
			}),
			components.CreateChatMessagesUser(components.ChatUserMessage{
				Role:    components.ChatUserMessageRoleUser,
				Content: components.CreateChatUserMessageContentStr(user),
			}),
		},
	}

	if c.opts.ReasoningEffort != "" {
		effort := components.ChatRequestReasoningEffort(c.opts.ReasoningEffort)
		req.ReasoningEffort = optionalnullable.From(&effort)
	}

	send := func() (string, error) {
		res, err := c.sdk.Chat.Send(ctx, req, nil)
		if err != nil {
			if retryable(ctx, err) {
				return "", err
			}
			return "", backoff.Permanent(err)
		}
		text, err := textOf(res)
		if err != nil {
			// A malformed or empty completion is not a transport failure, so retrying it
			// just pays for the same answer again.
			return "", backoff.Permanent(err)
		}
		return text, nil
	}

	if c.opts.MaxRetries <= 0 {
		return send()
	}
	return backoff.Retry(ctx, send,
		backoff.WithBackOff(backoff.NewExponentialBackOff()),
		backoff.WithMaxTries(uint(c.opts.MaxRetries)+1))
}

// textOf pulls the assistant text out of a response.
//
// The SDK models the response as a union of a completed result and an event stream,
// because the same call streams when asked to. This client never asks, so an event stream
// here means the request was not shaped the way this code believes it was - worth an
// explicit error rather than a nil dereference.
func textOf(res *operations.SendChatCompletionRequestResponse) (string, error) {
	if res == nil {
		return "", errors.New("openrouter: empty response")
	}
	if res.ChatResult == nil {
		return "", errors.New("openrouter: response was not a completion")
	}
	if len(res.ChatResult.Choices) == 0 {
		return "", errors.New("openrouter: response carried no choices")
	}

	msg := res.ChatResult.Choices[0].Message
	content, ok := msg.Content.Get()
	if !ok || content == nil {
		// A refusal is the interesting case here: the model declined, and reporting
		// "empty response" would send the operator looking for a network fault.
		if refusal, ok := msg.Refusal.Get(); ok && refusal != nil && *refusal != "" {
			return "", fmt.Errorf("openrouter: model refused: %s", *refusal)
		}
		return "", errors.New("openrouter: completion had no content")
	}
	if content.Str == nil {
		return "", errors.New("openrouter: completion content was not text")
	}
	return *content.Str, nil
}

// retryable reports whether an error is worth another attempt: rate limiting and server
// faults are, a bad request or a rejected key never will be.
//
// An error the SDK does not model as an APIError is a transport failure - a dropped
// connection, a DNS blip - which is retryable. A context cancellation is not, and is
// checked first because it otherwise falls into that same bucket and a Ctrl-C would then
// retry five times before giving up.
func retryable(ctx context.Context, err error) bool {
	// Ask the PARENT CONTEXT, never the returned error. Both a Ctrl-C and the HTTP
	// client's own per-request timeout surface as context.DeadlineExceeded/Canceled, and
	// they need opposite answers: a cancelled run must stop, while a single call that ran
	// out of time is the most retryable failure there is. Matching on the error treated
	// the second as the first, which is how one slow combining pass discarded 86 calls
	// that had already succeeded and been paid for.
	if ctx.Err() != nil {
		return false
	}
	var apiErr *sdkerrors.APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
}
