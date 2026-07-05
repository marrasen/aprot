package aprot

// Blob is an RPC result that can be delivered as a WebSocket binary frame.
//
// When the active transport supports binary frames, a Blob result is sent on
// the binary side channel and received by generated TypeScript clients as a
// Blob. Transports without binary support fall back to the JSON representation,
// with Data encoded by the JSON package (base64 for []byte).
type Blob struct {
	ContentType string `json:"contentType,omitempty"`
	Data        []byte `json:"data"`
}
