package analytics

import (
	"context"
	"time"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/store"
)

// Store serves the usage plane from the analytics service and everything else
// from the store it wraps.
//
// The embedded store.Store supplies every method this type does not name, so
// tenants, keys, providers, routing rules, quotas, webhooks and events keep
// answering from memory at memory's latency. The four overridden below are the
// whole usage domain: what GET /v1/usage reports, what GW-4 measures a quota
// against, and what a restart would otherwise lose.
//
// Composition lives here rather than as a general facility in the store package
// because there is exactly one split worth making and this is it. A generic
// "combine two stores" abstraction would have to describe which half owns each
// of some thirty-five methods, which is a larger thing to get wrong than the
// duplication it would save.
type Store struct {
	store.Store
	client *Client
}

// NewStore composes a store from an in-process one and an analytics client.
func NewStore(inner store.Store, client *Client) *Store {
	return &Store{Store: inner, client: client}
}

// Kind names both halves, because both are true.
//
// Reporting "analytics" alone would tell an operator reading /v1/health that
// the deployment is durable, when a restart still loses every tenant and key.
// Reporting "memory" alone would say usage is lost on restart, when it is the
// one thing that is not. The compound answer is the only one that misleads
// nobody, and GW-9 puts it in /v1/meta for exactly this kind of question.
func (s *Store) Kind() string { return "memory+analytics" }

// RecordUsage is deliberately not passed through unavailable below. Its caller
// is obs.Telemetry, which reads Permanent() off the client's own error to decide
// between retrying and dropping; an *apierr.Error in front of that would make
// every failure look alike and a malformed record would be retried forever.
func (s *Store) RecordUsage(ctx context.Context, rec *store.UsageRecord) error {
	return s.client.RecordUsage(ctx, rec)
}

func (s *Store) Usage(
	ctx context.Context, tenantID string, since, until time.Time,
) (store.UsageTotals, error) {
	totals, err := s.client.Usage(ctx, tenantID, since, until)
	return totals, unavailable(err)
}

func (s *Store) KeyUsage(
	ctx context.Context, tenantID, keyPrefix string, since, until time.Time,
) (store.UsageTotals, error) {
	totals, err := s.client.KeyUsage(ctx, tenantID, keyPrefix, since, until)
	return totals, unavailable(err)
}

func (s *Store) UsageBreakdown(
	ctx context.Context, tenantID string, since, until time.Time, groupBy string,
) ([]store.UsageBucket, error) {
	buckets, err := s.client.UsageBreakdown(ctx, tenantID, since, until, groupBy)
	return buckets, unavailable(err)
}

// unavailable classifies a failed usage read as a dependency being down rather
// than a gateway bug.
//
// Before the usage plane moved off the in-process store these reads could not
// fail, so an unclassified error became apierr.From's opaque 500. That is now
// the wrong answer twice over: it tells a caller the gateway is broken when the
// gateway is fine, and 500 is the one class a well-written client does not
// retry — which is exactly what it should do while analytics restarts.
//
// No Retry-After: GW-7.AC-6 makes that header the signal for a rate limit
// specifically, and there is no honest number to put in it here. The cause is
// attached for the log line and never reaches the caller's body, which stays
// the fixed sentence below (GW-14).
func unavailable(err error) error {
	if err == nil {
		return nil
	}
	return apierr.Unavailable(
		"Usage accounting is temporarily unavailable. The request itself was unaffected.",
	).WithCause(err)
}
