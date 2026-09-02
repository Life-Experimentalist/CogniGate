// Package analytics is the gateway's client for the CogniGate analytics
// service, the JVM process that owns durable usage storage.
//
// Only the usage plane lives here. Tenants, keys, routing rules and quotas stay
// in the gateway's own store: they are read to authenticate and route, so
// putting a network hop between a caller and its own credentials would make
// every request depend on a second process being up. Usage is the opposite
// shape — written once per request, off the critical path, and the one thing a
// restart must not lose (GW-11).
package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/store"
)

const (
	// maxErrorBody bounds how much of an unsuccessful response is quoted back in
	// an error. Enough to carry the service's own message, not enough for a
	// proxy's HTML error page to fill the log.
	maxErrorBody = 512
	// maxResponseBytes bounds a successful body. A breakdown is one row per
	// distinct model, provider or key, so this is orders of magnitude of slack.
	maxResponseBytes = 4 << 20
)

// Client talks to the analytics service's usage API.
type Client struct {
	base  string
	token string
	hc    *http.Client
}

// NewClient validates the configured endpoint and builds a client for it.
//
// The URL is parsed here rather than at the first request so that a typo stops
// the process at startup with a message naming the setting, instead of becoming
// a warning on the telemetry path that nobody reads until the invoice is wrong.
func NewClient(cfg config.Analytics) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing analytics.base_url %q: %w", cfg.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("analytics.base_url %q must be an http or https URL", cfg.BaseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("analytics.base_url %q names no host", cfg.BaseURL)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		base:  strings.TrimRight(cfg.BaseURL, "/"),
		token: cfg.Token,
		hc:    &http.Client{Timeout: timeout},
	}, nil
}

// StatusError is an analytics response the client could not use.
type StatusError struct {
	Op     string
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("analytics %s: %d %s", e.Op, e.Status, http.StatusText(e.Status))
	}
	return fmt.Sprintf("analytics %s: %d %s: %s", e.Op, e.Status, http.StatusText(e.Status), e.Body)
}

// Permanent reports whether retrying this request unchanged could ever succeed.
//
// It is what stops one malformed record from blocking every record queued
// behind it: a 4xx says the request itself is wrong, so replaying it will be
// wrong again and it should be dropped instead of retried forever. The two
// exceptions describe a moment rather than a request. A 5xx, and every
// transport error — which never reaches this type — is worth retrying, because
// that is what an analytics service being restarted looks like.
func (e *StatusError) Permanent() bool {
	switch e.Status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return e.Status >= 400 && e.Status < 500
}

// RecordUsage persists one metered request.
//
// The service is idempotent on request_id and answers 200 rather than 201 when
// it already holds the record. Both are success here: a retry that discovers
// its own earlier write has delivered the record, and calling that a failure
// would wedge the queue behind something already stored.
func (c *Client) RecordUsage(ctx context.Context, rec *store.UsageRecord) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encoding the usage record: %w", err)
	}
	_, err = c.do(ctx, http.MethodPost, "/api/v1/usage", nil, body)
	return err
}

func (c *Client) Usage(ctx context.Context, tenantID string, since, until time.Time) (store.UsageTotals, error) {
	return c.totals(ctx, url.Values{
		"tenant_id": {tenantID},
		"since":     {stamp(since)},
		"until":     {stamp(until)},
	})
}

func (c *Client) KeyUsage(ctx context.Context, tenantID, keyPrefix string, since, until time.Time) (store.UsageTotals, error) {
	return c.totals(ctx, url.Values{
		"tenant_id":  {tenantID},
		"key_prefix": {keyPrefix},
		"since":      {stamp(since)},
		"until":      {stamp(until)},
	})
}

func (c *Client) totals(ctx context.Context, q url.Values) (store.UsageTotals, error) {
	var out store.UsageTotals
	body, err := c.do(ctx, http.MethodGet, "/api/v1/usage/totals", q, nil)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return store.UsageTotals{}, fmt.Errorf("decoding the usage totals: %w", err)
	}
	return out, nil
}

func (c *Client) UsageBreakdown(
	ctx context.Context, tenantID string, since, until time.Time, groupBy string,
) ([]store.UsageBucket, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/v1/usage/breakdown", url.Values{
		"tenant_id": {tenantID},
		"since":     {stamp(since)},
		"until":     {stamp(until)},
		"group_by":  {groupBy},
	}, nil)
	if err != nil {
		return nil, err
	}
	var out []store.UsageBucket
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding the usage breakdown: %w", err)
	}
	return out, nil
}

// stamp renders a window bound. UTC with an explicit offset, so the two sides
// cannot disagree about which day a record fell in.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// do issues one request, having already turned an unsuccessful status into a
// *StatusError so that callers never have to read status codes themselves.
func (c *Client) do(
	ctx context.Context, method, path string, query url.Values, body []byte,
) ([]byte, error) {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	op := method + " " + path

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return nil, fmt.Errorf("analytics %s: %w", op, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("analytics %s: %w", op, err)
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
		return nil, &StatusError{Op: op, Status: res.StatusCode,
			Body: strings.TrimSpace(string(snippet))}
	}

	out, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("analytics %s: reading the response: %w", op, err)
	}
	return out, nil
}
