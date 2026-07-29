package server

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/utils"
)

// The accessors below exist because fasthttp hands back strings that point into
// a pooled request buffer. They are valid for the life of the request and no
// longer: once the connection is recycled the bytes underneath are overwritten
// by whatever the next request happened to contain.
//
// That makes the failure mode remote from its cause. A tenant id read with
// c.Params and stored verbatim keeps working for as long as the buffer is idle,
// then silently becomes a different string — a tenant id whose prefix has turned
// into a fragment of some later request's URL — and every credential belonging
// to that tenant stops resolving. Nothing in the store did anything wrong, and
// the request that broke it is long gone by the time anyone notices.
//
// Copying at the point of extraction is the only place this can be got right
// once. Handlers downstream then hold ordinary Go strings and need not reason
// about request lifetimes at all, which is the property worth paying one
// allocation per parameter for.

// param returns a routing parameter the caller owns.
func param(c *fiber.Ctx, name string) string {
	return utils.CopyString(c.Params(name))
}

// query returns a query-string value the caller owns.
func query(c *fiber.Ctx, name string, def ...string) string {
	return utils.CopyString(c.Query(name, def...))
}

// header returns a request header the caller owns.
func header(c *fiber.Ctx, name string) string {
	return utils.CopyString(c.Get(name))
}
