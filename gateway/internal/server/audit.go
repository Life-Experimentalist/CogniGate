package server

import (
	"log/slog"
	"net/http"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/store"
)

// auditMutations records every admin-plane write in the append-only log GW-6
// requires.
//
// It is middleware rather than a call inside each handler because the promise
// is "no mutation goes unrecorded", and a promise that depends on every future
// handler remembering to opt in is not one. Adding a route is now enough to get
// it audited.
//
// It runs after c.Next() so the outcome is known. Refused writes are recorded
// alongside accepted ones: an attempt to reach another tenant, or to mint a
// root key without root scope, is precisely what someone reads this log to
// find, and a log that only contained successes would be silent about it.
//
// Nothing from the request body is stored. A provider registration carries
// plaintext upstream credentials, and rather than maintain a per-route list of
// which bodies are safe, the log keeps who, which verb, which path and what
// happened. GW-14 governs this store like every other.
func (s *Server) auditMutations() fiber.Handler {
	return func(c *fiber.Ctx) error {
		err := c.Next()

		action, mutating := auditAction(c.Method())
		if !mutating {
			return err
		}
		key := httpx.Key(c)
		if key == nil {
			// Authentication refused before any handler ran, so there is no
			// actor to attribute and nothing was changed.
			return err
		}

		// A handler that returns a raw error has not reached Fiber's error
		// handler yet, so the response still carries its default status. The
		// registry is what that error is about to become.
		status := c.Response().StatusCode()
		if err != nil {
			status = apierr.From(err).Status
		}

		entry := &store.AuditEntry{
			Actor:      key.Prefix,
			ActorKeyID: key.ID,
			ActorScope: key.Scope,
			Action:     action,
			Resource:   path(c),
			TenantID:   param(c, "tenant"),
			Status:     status,
			RequestID:  httpx.RequestID(c),
		}

		ctx, cancel := s.opContext(c)
		defer cancel()

		// A store that cannot record the write does not undo it: the change has
		// already happened, and answering 500 to a caller whose request
		// succeeded would be a worse lie than the missing log line. It is logged
		// at error level so the gap is visible to whoever operates the process.
		if auditErr := s.Store.RecordAudit(ctx, entry); auditErr != nil {
			s.Logger.Error("could not record admin audit entry",
				slog.String("action", entry.Action),
				slog.String("resource", entry.Resource),
				slog.String("error", auditErr.Error()))
		}
		return err
	}
}

// auditAction maps a method onto the vocabulary the log uses. A method absent
// from this table is a read, and reads are not audited: the log exists to
// answer "what changed", and burying that under a GET per dashboard refresh
// would make it unreadable for the one question it is for.
func auditAction(method string) (string, bool) {
	switch method {
	case http.MethodPost:
		return "create", true
	case http.MethodPut:
		return "upsert", true
	case http.MethodPatch:
		return "update", true
	case http.MethodDelete:
		return "delete", true
	default:
		return "", false
	}
}

// listAudit serves GET /admin/v1/audit.
//
// Root scope only, and deliberately not narrowable to one tenant. A
// tenant-scoped key reading a filtered view of this log would learn which of
// its own writes an operator had reversed, and the log's value comes from the
// people it describes not controlling it.
func (s *Server) listAudit(c *fiber.Ctx) error {
	if err := requireRoot(c); err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	entries, err := s.Store.ListAudit(ctx)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return sendPage(c, entries, func(e *store.AuditEntry) string { return e.ID })
}
