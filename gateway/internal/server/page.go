package server

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/httpx"
)

// sendPage is the whole body of every admin list handler: paginate, or answer
// with the reason the paging arguments could not be honoured. It is a function
// rather than a method on Server because Go methods cannot take type
// parameters.
func sendPage[T any](c *fiber.Ctx, items []T, idOf func(T) string) error {
	page, err := paginate(c, items, idOf)
	if err != nil {
		return httpx.Fail(c, err)
	}
	return c.JSON(page)
}

// GW-6 pins the page size: 50 by default, 200 at most. The ceiling exists so a
// single call cannot ask the control plane to materialise an unbounded list —
// the reason to paginate at all.
const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

// paginate wraps an already-ordered slice in the GW-6 list envelope, honouring
// ?limit= and ?after=.
//
// The cursor is the id of the last item the caller saw rather than a numeric
// offset. An offset quietly skips a row whenever something ahead of it is
// deleted between two pages, and "the page you did not ask for is the one you
// did not get" is a poor property for a control plane and a worse one for an
// audit log.
//
// It is a generic over the element type with an explicit id accessor rather
// than an interface the stored types implement: those types are the wire format
// of this API, and adding a method to them for the benefit of one helper would
// put pagination's concerns into the persistence layer.
func paginate[T any](c *fiber.Ctx, items []T, idOf func(T) string) (fiber.Map, error) {
	limit, err := pageLimit(c)
	if err != nil {
		return nil, err
	}
	if items == nil {
		// A nil slice marshals as null, and a client parsing `data` as an array
		// should not have to special-case the empty collection.
		items = []T{}
	}

	if after := strings.TrimSpace(query(c, "after")); after != "" {
		at := -1
		for i, item := range items {
			if idOf(item) == after {
				at = i
				break
			}
		}
		// An unknown cursor is refused rather than treated as "start from the
		// beginning". Silently answering with page one looks like success and
		// hands back everything the caller already has, so a paging loop that
		// lost its place would repeat forever instead of reporting a problem.
		if at < 0 {
			return nil, apierr.
				InvalidRequest("Unknown cursor: no item with that id in this collection.").
				WithParam("after")
		}
		items = items[at+1:]
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return fiber.Map{"object": "list", "data": items, "has_more": hasMore}, nil
}

// pageLimit reads ?limit=, refusing anything outside the range instead of
// clamping. Clamping a request for 5000 to 200 returns a short page with
// has_more set, which is indistinguishable from a genuine last page and leaves
// the caller believing it asked for something it did not get.
func pageLimit(c *fiber.Ctx) (int, error) {
	raw := strings.TrimSpace(query(c, "limit"))
	if raw == "" {
		return defaultPageLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxPageLimit {
		return 0, apierr.
			InvalidRequest(fmt.Sprintf("limit must be an integer between 1 and %d.", maxPageLimit)).
			WithParam("limit")
	}
	return n, nil
}
