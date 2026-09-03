package server

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/capture"
	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/store"
)

// debugCaptureOn is the only value X-CogniGate-Debug-Capture ever carries. The
// header is present or it is not; there is no "off", because a header saying
// nothing is being retained would appear on every response of every deployment
// and mean nothing on any of them.
const debugCaptureOn = "on"

// newCaptureStore builds the GW-14 capture store.
//
// Unlike the response cache, this is never nil. Capture has no deployment
// switch to be off — it is enabled per tenant, by an audited admin action —
// so the store always exists and is simply empty until someone asks for it.
func newCaptureStore(d config.Debug) *capture.Store {
	return capture.New(d.MaxBytesPerTenant)
}

// captureDebug is GW-14's data-plane middleware: the header on every response
// while a tenant's capture is on, and the capture itself on a sampled fraction
// of them.
//
// It is middleware rather than a branch inside the completion handler because
// the header is owed on *every* data-plane response for that tenant — a
// rejected request, a quota refusal, an upstream_exhausted, a stream — and a
// promise phrased "every response" cannot be kept by a handler that only runs
// for some of them. The same position is what lets the capture record errors,
// which is what someone who turned capture on to investigate a failure is
// actually looking for.
//
// A streamed response body is never captured — only the request that asked for
// it. Reading the body of a streaming response does not observe it, it consumes
// it: fasthttp's Response.Body drains the stream into a buffer and closes it, so
// asking would turn every streamed request into a buffered one for as long as
// capture was on. GW-14 does not ask for the response half, and a capture
// feature that quietly changed how the gateway served the request it was
// capturing would be worse than not having it.
func (s *Server) captureDebug() fiber.Handler {
	return func(c *fiber.Ctx) error {
		tenant := httpx.Tenant(c)
		if tenant == nil || !tenant.DebugCapture.Enabled {
			return c.Next()
		}

		// Set before the handler runs, so it survives an error unwinding
		// through Fiber's error handler and is already on a streamed response
		// by the time its first byte is written.
		c.Set(httpx.HeaderDebugCapture, debugCaptureOn)

		// The request body is read here because fasthttp reuses its buffer once
		// the handler returns, and sampling is decided here for the same
		// reason: after c.Next() there may be nothing left to copy.
		var request []byte
		sample := rand.Float64() < s.captureSampleRate(tenant)
		if sample {
			request = append([]byte(nil), c.Body()...)
		}

		err := c.Next()

		if !sample {
			return err
		}
		status := c.Response().StatusCode()
		if err != nil {
			status = apierr.From(err).Status
		}
		// IsBodyStream has to be asked before Body: on a streamed response Body
		// is not a read but a drain, and the relay would deliver nothing.
		var response []byte
		if !c.Response().IsBodyStream() {
			response = c.Response().Body()
		}
		// No request body, nothing to capture. That is every GET on this group —
		// /v1/models, /v1/meta, /v1/health, /v1/usage — and capturing those would
		// spend a tenant's byte budget on catalogue pages, evicting the
		// completions capture was turned on for. The header is still owed on
		// them, and was set above.
		if len(request) == 0 {
			return err
		}

		now := time.Now().UTC()
		s.captures.Put(tenant.ID, capture.Entry{
			ID:        store.NewID(store.IDCapture),
			RequestID: httpx.RequestID(c),
			At:        now,
			ExpiresAt: now.Add(s.captureTTL(tenant)),
			Model:     httpx.GetOutcome(c).Model,
			Status:    status,
			Request:   request,
			Response:  append([]byte(nil), response...),
		})
		return err
	}
}

// captureTTL and captureSampleRate resolve a tenant's policy against the
// deployment's, applying the ceiling here as well as in validation for the
// reason cacheTTL gives: a value stored by an older, laxer build must not
// outlive the rule that would refuse it today. A zero field means the
// deployment default, never zero itself.
func (s *Server) captureTTL(t *store.Tenant) time.Duration {
	ttl := s.Config.Debug.DefaultTTL
	if t != nil && t.DebugCapture.TTLSeconds > 0 {
		ttl = time.Duration(t.DebugCapture.TTLSeconds) * time.Second
	}
	if max := s.Config.Debug.MaxTTL; ttl > max {
		return max
	}
	return ttl
}

func (s *Server) captureSampleRate(t *store.Tenant) float64 {
	if t != nil && t.DebugCapture.SampleRate > 0 {
		return t.DebugCapture.SampleRate
	}
	return s.Config.Debug.DefaultSampleRate
}

// validateTenantDebugCapture refuses a policy the deployment will not honour.
//
// The TTL ceiling has its own error code rather than reusing invalid_request,
// because it is the one limit in GW-14 a caller is most likely to hit
// deliberately — someone who wants a week of captures — and telling them "no,
// and here is the maximum" is more useful than a generic rejection. It refuses
// rather than clamping, so nobody configures 7 days, reads back 3, and believes
// the first number.
func (s *Server) validateTenantDebugCapture(p store.TenantDebugCapture) *apierr.Error {
	if p.TTLSeconds < 0 {
		return apierr.InvalidRequest("debug_capture.ttl_seconds must not be negative.").
			WithParam("debug_capture.ttl_seconds")
	}
	if time.Duration(p.TTLSeconds)*time.Second > s.Config.Debug.MaxTTL {
		return apierr.CaptureTTLTooLong(int(s.Config.Debug.MaxTTL.Hours()))
	}
	if p.SampleRate < 0 || p.SampleRate > 1 {
		return apierr.InvalidRequest("debug_capture.sample_rate must be within 0..1.").
			WithParam("debug_capture.sample_rate")
	}
	return nil
}

// captureWarnings is what the admin response echoes back alongside the updated
// tenant.
//
// GW-14 requires enabling `sample_rate: 1.0` to produce a warning, and the
// reason is worth saying out loud in the response rather than only in the
// documentation: at 1.0 the gateway retains every prompt and every completion
// that tenant sends for the length of the TTL, which is the exact opposite of
// what the rest of the product promises. Someone who meant it will not mind
// reading why; someone who typed it by accident is the person this is for.
func (s *Server) captureWarnings(p store.TenantDebugCapture) []string {
	if !p.Enabled || p.SampleRate < 1 {
		return nil
	}
	return []string{fmt.Sprintf(
		"debug_capture.sample_rate is 1.0: every request and response body for this "+
			"tenant will be retained for %s and readable through the admin plane. "+
			"Prefer a sample below 1.0 unless a specific investigation needs all of it.",
		s.captureTTL(&store.Tenant{DebugCapture: p}))}
}

// listCaptures serves GET /admin/v1/tenants/{id}/captures.
//
// Admin plane only, and scoped like every other tenant resource: root, or that
// tenant's own admin key. This is the single outlet for captured content in the
// whole product. Nothing else — no log line, no metric label, no webhook, no
// usage record, no telemetry to the analytics service — carries it, which is
// what makes the rest of GW-14's content ban checkable rather than aspirational.
//
// The list is empty for a tenant whose capture has never been on, which is
// GW-14.AC-2, and empty again once the TTL has passed, which is AC-3.
func (s *Server) listCaptures(c *fiber.Ctx) error {
	id, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	ctx, cancel := s.opContext(c)
	defer cancel()
	if _, err := s.Store.GetTenant(ctx, id); err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", id))
	}

	entries := s.captures.List(id, time.Now().UTC())
	return sendPage(c, entries, func(e capture.Entry) string { return e.ID })
}

// sweepCaptures hard-deletes expired captures until the process stops.
//
// This is the only background loop in the gateway, and it earns the exception:
// everywhere else a TTL governs what is *served* — a stale catalogue, a cached
// answer — and letting the bytes sit until something reads them costs nothing
// but memory. Here the TTL is a deletion promise made about content the
// operator explicitly agreed to keep for a bounded time, and an operator who
// enables capture, sends traffic and turns it off again would otherwise leave
// that content resident until the process ended, because nothing would ever
// read it back.
func (s *Server) sweepCaptures(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopSweep:
			return
		case now := <-ticker.C:
			s.captures.Sweep(now.UTC())
		}
	}
}
