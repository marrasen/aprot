package client

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// Blob is binary data returned by a handler that returns aprot.Blob.
//
// The server sends a top-level Blob result as a binary WebSocket frame, or
// as a JSON envelope with base64 data on the byte-stream transport. Both
// arrive here as the same Blob. A Blob nested inside another type travels as
// plain JSON and decodes the same way.
type Blob struct {
	ContentType string `json:"contentType,omitempty"`
	Data        []byte `json:"data"`
}

// UnmarshalJSON accepts both the plain shape and the server's
// {"$blob": {...}} envelope for a top-level result.
func (b *Blob) UnmarshalJSON(data []byte) error {
	var probe map[string]jsontext.Value
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if inner, ok := probe["$blob"]; ok && len(probe) == 1 {
		data = inner
	}
	type plain Blob
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*b = Blob(p)
	return nil
}
