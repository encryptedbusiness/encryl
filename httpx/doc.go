// Package httpx parses WebSocket upgrade requests and builds the 101 response.
//
// It works on parsed HTTP headers as well as raw requests, so an attached
// framework can hand off a socket without rebuilding or reparsing HTTP.
//
//	req, ok := httpx.ParseRequest(raw)
//	resp := httpx.Response(req)
package httpx
