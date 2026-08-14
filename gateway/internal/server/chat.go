package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/obs"
	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/routing"
	"github.com/cognigate/gateway/internal/store"
)

// chatEnvelope is the only part of a completion request the gateway parses.
//
// Everything else — messages, tools, temperature, provider extensions — is
// forwarded byte for byte. Routing needs the model and whether the caller wants
// a stream; parsing more would mean re-serialising content the gateway has no
// business touching (GW-14) and would silently drop fields it does not model.
type chatEnvelope struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func (s *Server) handleChatCompletions(c *fiber.Ctx) error {
	body := c.Body()
	if len(body) == 0 {
		return httpx.Fail(c, apierr.InvalidRequest("A JSON request body is required."))
	}

	var env chatEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return httpx.Fail(c, apierr.InvalidRequest("Request body is not valid JSON.").WithCause(err))
	}
	if env.Model == "" {
		return httpx.Fail(c, apierr.InvalidRequest("The \"model\" field is required.").WithParam("model"))
	}

	tenantID := httpx.TenantID(c)

	quota, err := s.evaluateQuota(c.UserContext(), requestScope(c))
	if err != nil {
		return httpx.Fail(c, err)
	}
	c.Set(httpx.HeaderQuotaState, quota.State)
	if quota.Reject != nil {
		s.recordRejection(c, env.Model, quota.Reject)
		return httpx.Fail(c, quota.Reject)
	}

	// The body is copied because fasthttp reuses its buffer once the handler
	// returns, and a streamed response outlives the handler.
	payload := append([]byte(nil), body...)

	if env.Stream {
		return s.streamCompletion(c, tenantID, env.Model, payload)
	}
	return s.completion(c, tenantID, env.Model, payload)
}

// completion serves a buffered chat completion.
func (s *Server) completion(c *fiber.Ctx, tenantID, requested string, body []byte) error {
	ctx, cancel := s.opContext(c)
	defer cancel()

	started := time.Now()
	result, err := s.Dispatcher.Dispatch(ctx, tenantID, requested, body, false, "/chat/completions")
	if err != nil {
		s.recordRejection(c, requested, err)
		return httpx.Fail(c, s.timeoutAware(ctx, err))
	}
	defer result.Response.Close()

	s.applyRoutingHeaders(c, result)
	s.meter(c, result, requested, result.Response.Usage, false, fiber.StatusOK, time.Since(started))

	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return c.Status(fiber.StatusOK).Send(result.Response.Body)
}

// streamCompletion relays an SSE completion.
//
// The upstream body is handed to Fiber's stream writer, which runs after this
// handler returns — so ownership of Close moves into the writer. Deferring it
// here would close the upstream before a single token had been relayed.
func (s *Server) streamCompletion(c *fiber.Ctx, tenantID, requested string, body []byte) error {
	// No request deadline on a stream: it is bounded by silence
	// (stream_idle_timeout), not by duration.
	ctx := c.UserContext()

	started := time.Now()
	result, err := s.Dispatcher.Dispatch(ctx, tenantID, requested, body, true, "/chat/completions")
	if err != nil {
		// Nothing has been written yet, so this is still an ordinary HTTP error
		// with a status line. GW-3's "fall back only before the first byte" rule
		// is satisfied by construction: the cascade lives entirely above here.
		s.recordRejection(c, requested, err)
		return httpx.Fail(c, s.timeoutAware(ctx, err))
	}

	s.applyRoutingHeaders(c, result)
	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")
	// Proxies that buffer will hold the whole completion and defeat streaming;
	// this is the header nginx and its imitators honour.
	c.Set("X-Accel-Buffering", "no")
	c.Status(fiber.StatusOK)

	// Everything the usage record needs is read here, while the request is still
	// alive, and carried into the writer by the closure. Reading any of it from
	// the Fiber context inside the writer would be reading a recycled request:
	// the handler has returned by the time the writer runs.
	var (
		requestID       = httpx.RequestID(c)
		clientRequestID = httpx.ClientRequestID(c)
		prefix          = keyPrefix(c)
		logger          = s.Logger
	)

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer result.Response.Close()

		usage, stalled, relayErr := relaySSE(w, result.Response.Stream, s.Config.Limits.StreamIdleTimeout)

		switch {
		case stalled:
			// The status line went out long ago, so the only way to report this
			// is in-band. GW-7 gives the stall its own code precisely because it
			// can never be an HTTP status.
			writeStreamError(w, apierr.UpstreamError("The upstream stopped sending data.").
				WithCause(relayErr), apierr.CodeUpstreamStreamStalled, requestID)
			logger.Warn("upstream stream stalled",
				slog.String("request_id", requestID),
				slog.String("served_by", result.Candidate.ServedBy()))
		case relayErr != nil && !errors.Is(relayErr, io.EOF):
			// The upstream died part-way through — a dropped connection, a reset,
			// a provider that panicked. Without a terminal event this is
			// indistinguishable from a completion that simply ended, and GW-3.AC-7
			// forbids leaving the caller to guess: a truncated answer must announce
			// itself. If the reason the relay stopped is that the *client* hung up,
			// this frame goes to a closed socket and is discarded, which costs
			// nothing and is not worth a special case.
			writeStreamError(w, apierr.UpstreamError("The upstream ended the stream before it completed.").
				WithCause(relayErr), apierr.CodeUpstreamError, requestID)
			logger.Warn("stream relay ended early",
				slog.String("request_id", requestID),
				slog.String("served_by", result.Candidate.ServedBy()),
				slog.String("error", relayErr.Error()))
		}

		s.meterStream(requestID, clientRequestID, tenantID, prefix, result, requested, usage, time.Since(started))
	})

	return nil
}

// relaySSE copies an event stream through, watching for silence.
//
// It returns the usage block if the upstream sent one, whether the stream
// stalled, and the error that ended the relay.
func relaySSE(w *bufio.Writer, src io.ReadCloser, idleTimeout time.Duration) (*provider.Usage, bool, error) {
	if idleTimeout <= 0 {
		idleTimeout = 60 * time.Second
	}

	var stalled atomic.Bool
	// Closing the body is what unblocks the read: there is no way to interrupt
	// an in-flight Read otherwise, and a goroutine parked on a dead connection
	// would leak for as long as the process lives.
	watchdog := time.AfterFunc(idleTimeout, func() {
		stalled.Store(true)
		_ = src.Close()
	})
	defer watchdog.Stop()

	var usage *provider.Usage
	reader := bufio.NewReaderSize(src, 16*1024)

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			watchdog.Reset(idleTimeout)

			if u := usageFromEvent(line); u != nil {
				usage = u
			}
			if _, werr := w.Write(line); werr != nil {
				// The client hung up. Nothing to report to anyone but the log.
				return usage, false, werr
			}
			// Flushing per line is the point of a stream; buffering would
			// reintroduce exactly the latency the caller asked to avoid.
			if werr := w.Flush(); werr != nil {
				return usage, false, werr
			}
		}
		if err != nil {
			if stalled.Load() {
				return usage, true, err
			}
			if errors.Is(err, io.EOF) {
				return usage, false, nil
			}
			return usage, false, err
		}
	}
}

// sseUsage is the only field read out of a streamed chunk. Decoding into this
// shape rather than the full chunk means completion text is never materialised
// into a Go value on the metering path (GW-14).
type sseUsage struct {
	Usage *provider.Usage `json:"usage"`
}

func usageFromEvent(line []byte) *provider.Usage {
	const prefix = "data: "
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte(prefix)) {
		return nil
	}
	payload := bytes.TrimSpace(trimmed[len(prefix):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil
	}
	var parsed sseUsage
	if json.Unmarshal(payload, &parsed) != nil {
		return nil
	}
	if parsed.Usage == nil || parsed.Usage.TotalTokens == 0 {
		return nil
	}
	return parsed.Usage
}

// writeStreamError emits a terminal error event in the GW-7 envelope, then the
// [DONE] sentinel so a well-behaved client's loop still terminates.
func writeStreamError(w *bufio.Writer, e *apierr.Error, code, requestID string) {
	envelope := e.Envelope(requestID)
	envelope.Error.Code = code
	payload, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
	fmt.Fprint(w, "data: [DONE]\n\n")
	_ = w.Flush()
}

// --- response headers and metering ------------------------------------------

// applyRoutingHeaders sets the extension headers that describe how a successful
// request was actually served.
func (s *Server) applyRoutingHeaders(c *fiber.Ctx, result *routing.Result) {
	c.Set(httpx.HeaderServedBy, result.Candidate.ServedBy())
	c.Set(httpx.HeaderFallbackDepth, strconv.Itoa(result.Depth))
	if result.Candidate.Entry.Deprecated {
		c.Set(httpx.HeaderDeprecation, "true")
	}
}

// meter records one served request: metrics now, usage row asynchronously.
func (s *Server) meter(c *fiber.Ctx, result *routing.Result, requested string, usage *provider.Usage, streamed bool, status int, elapsed time.Duration) {
	s.record(httpx.RequestID(c), httpx.ClientRequestID(c), httpx.TenantID(c), keyPrefix(c),
		result, requested, usage, streamed, status, elapsed)
}

// meterStream is meter's counterpart for the streaming path, where the Fiber
// context's own buffers are no longer safe to read: the handler has returned and
// fasthttp may have recycled them. So the identity of the request is passed in
// as plain strings, captured by streamCompletion while the request was alive. A
// streamed completion is metered exactly like a buffered one — anything less and
// the most common shape of traffic would be missing from usage, quota and
// billing.
func (s *Server) meterStream(
	requestID, clientRequestID, tenantID, prefix string,
	result *routing.Result, requested string,
	usage *provider.Usage, elapsed time.Duration,
) {
	s.record(requestID, clientRequestID, tenantID, prefix,
		result, requested, usage, true, fiber.StatusOK, elapsed)
}

func (s *Server) record(
	requestID, clientRequestID, tenantID, prefix string,
	result *routing.Result, requested string,
	usage *provider.Usage, streamed bool, status int, elapsed time.Duration,
) {
	cand := result.Candidate
	cost := result.CostUSD(usage)

	if s.Metrics != nil {
		s.Metrics.UpstreamDuration.
			WithLabelValues(cand.Provider, cand.Model).
			Observe(result.Response.Latency.Seconds())
		if usage != nil {
			s.Metrics.Tokens.WithLabelValues(tenantID, cand.Provider, cand.Model, obs.TokenKindPrompt).
				Add(float64(usage.PromptTokens))
			s.Metrics.Tokens.WithLabelValues(tenantID, cand.Provider, cand.Model, obs.TokenKindCompletion).
				Add(float64(usage.CompletionTokens))
		}
		if cost > 0 {
			s.Metrics.Cost.WithLabelValues(tenantID, cand.Provider, cand.Model).Add(cost)
		}
		if result.Depth > 0 {
			s.Metrics.FallbackCascades.WithLabelValues(tenantID, strconv.Itoa(result.Depth)).Inc()
		}
	}

	rec := store.UsageRecord{
		RequestID:       requestID,
		ClientRequestID: clientRequestID,
		TenantID:        tenantID,
		KeyPrefix:       prefix,
		Provider:        cand.Provider,
		Model:           cand.Model,
		RequestedModel:  requested,
		FallbackDepth:   result.Depth,
		CostUSD:         cost,
		Streamed:        streamed,
		StatusCode:      status,
		DurationMS:      elapsed.Milliseconds(),
		RecordedAt:      time.Now().UTC(),
	}
	if usage != nil {
		rec.PromptTokens = usage.PromptTokens
		rec.CompletionToken = usage.CompletionTokens
		rec.TotalTokens = usage.TotalTokens
	}
	if s.Telemetry != nil {
		s.Telemetry.Record(rec)
	}
}

// recordRejection logs a request that never reached a provider. It writes no
// usage row: nothing was consumed, and a row with zero tokens against no model
// would distort every breakdown it appears in.
func (s *Server) recordRejection(c *fiber.Ctx, requested string, err error) {
	e := apierr.From(err)
	s.Logger.Warn("request rejected",
		slog.String("request_id", httpx.RequestID(c)),
		slog.String("tenant", httpx.TenantID(c)),
		slog.String("requested_model", requested),
		slog.String("code", e.Code),
		slog.Int("status", e.Status),
		slog.String("cause", causeOf(e)))
}

func causeOf(e *apierr.Error) string {
	if e.Wrapped == nil {
		return ""
	}
	return e.Wrapped.Error()
}

// timeoutAware upgrades a generic dispatch failure to 504 when the reason every
// candidate failed was that the gateway ran out of time. Reporting that as
// upstream_exhausted would send an operator looking at provider health for a
// problem that is a deadline.
func (s *Server) timeoutAware(ctx context.Context, err error) error {
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return err
	}
	var e *apierr.Error
	if errors.As(err, &e) && e.Status != fiber.StatusBadGateway {
		return err
	}
	return apierr.GatewayTimeout(s.Config.Limits.RequestTimeout.Seconds()).WithCause(err)
}

func keyPrefix(c *fiber.Ctx) string {
	if k := httpx.Key(c); k != nil {
		return k.Prefix
	}
	return ""
}
