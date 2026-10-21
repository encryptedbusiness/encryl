//go:build linux

// Command fiber runs the TFB JSON/Plaintext shape on Fiber v3.
package main

import (
	"flag"
	"log"

	"github.com/encryptedbusiness/encryl/bench/http/internal/payload"
	"github.com/gofiber/fiber/v3"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18098", "listen address")
	flag.Parse()

	app := fiber.New(fiber.Config{
		ServerHeader: "encryl",
	})
	app.Get("/json", func(c fiber.Ctx) error {
		var buf [32]byte
		body := payload.AppendMessage(buf[:0], payload.Message{Message: "Hello, World!"})
		c.Response().Header.SetContentType("application/json")
		c.Response().Header.SetContentLength(len(body))
		c.Response().SetBodyRaw(body)
		return nil
	})
	app.Get("/plaintext", func(c fiber.Ctx) error {
		c.Response().Header.SetContentType("text/plain")
		c.Response().Header.SetContentLength(len(payload.Plaintext))
		c.Response().SetBodyRaw([]byte(payload.Plaintext))
		return nil
	})

	log.Printf("fiber benchmark listening on %s", *addr)
	log.Fatal(app.Listen(*addr, fiber.ListenConfig{DisableStartupMessage: true}))
}
