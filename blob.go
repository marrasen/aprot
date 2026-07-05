package aprot

import "reflect"

// blobType lets the generator recognize Blob response types.
var blobType = reflect.TypeOf(Blob{})

// isBlobResponse reports whether a handler's response type is Blob or *Blob,
// i.e. a result that opts into binary delivery (see asBlob for the runtime
// counterpart).
func isBlobResponse(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t == blobType
}

// blobTSWireShape is the TypeScript type of a Blob outside a top-level
// result position (nested in a struct, streamed, or used as a parameter),
// where it travels as plain JSON with base64 data. Only top-level results
// are delivered as binary frames and typed as the DOM Blob.
const blobTSWireShape = "{ contentType?: string; data: string }"

// Blob is an RPC result that is delivered to the client as raw binary data.
//
// Return Blob (or *Blob) from a handler to opt into binary delivery. On
// transports with a native binary channel (WebSocket) the payload is sent as
// a binary frame; on other transports (SSE, stream) it falls back to a JSON
// envelope carrying base64 data under a "$blob" marker. Generated TypeScript
// clients convert both encodings into a DOM Blob, so the client-visible
// result type does not depend on the transport.
//
// Binary delivery applies only to Blob as the top-level result of a unary
// handler (including server-driven subscription refreshes). A Blob nested
// inside another struct, streamed as an item, or passed as a parameter
// travels as plain JSON ({contentType, data} with base64 data).
type Blob struct {
	ContentType string `json:"contentType,omitempty"`
	Data        []byte `json:"data"`
}
