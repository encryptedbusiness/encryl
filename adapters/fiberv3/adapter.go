//go:build linux

package fiberadapter

import (
	encryl "github.com/encryptedbusiness/encryl"
	fasthttpadapter "github.com/encryptedbusiness/encryl/adapters/fasthttp"
	"github.com/gofiber/fiber/v3"
)

// Handler returns a Fiber v3 handler. Use it after routing/authentication
// middleware; the framework request is handed to encryl for the upgrade.
func Handler(srv *encryl.Server) fiber.Handler {
	return func(c fiber.Ctx) error {
		return fasthttpadapter.Upgrade(c.RequestCtx(), srv)
	}
}
