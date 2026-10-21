// Package payload contains the TFB JSON and plaintext bodies.
package payload

import (
	"github.com/mailru/easyjson/jwriter"
)

const (
	// Plaintext is the TFB plaintext response.
	Plaintext = "Hello, World!"
	// JSONContentLength is the encoded byte length of {"message":"Hello, World!"}.
	JSONContentLength = 27
)

// Message is the TFB JSON object.
type Message struct {
	Message string `json:"message"`
}

// MarshalEasyJSON is the zero-allocation generated-style encoder for Message.
func (m Message) MarshalEasyJSON(w *jwriter.Writer) {
	w.RawByte('{')
	w.RawString(`"message":`)
	w.String(m.Message)
	w.RawByte('}')
}

// AppendMessage serializes m into dst, which must have capacity for 27 bytes.
// It writes directly into dst and returns the new slice without allocating.
func AppendMessage(dst []byte, m Message) []byte {
	w := jwriter.Writer{}
	w.Buffer.Buf = dst[:0]
	m.MarshalEasyJSON(&w)
	return w.Buffer.Buf
}
