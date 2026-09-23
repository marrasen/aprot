package aprot

import "reflect"

// blobType lets the generator recognize Blob response types.
var blobType = reflect.TypeOf(Blob{})

// isBlobResponse reports whether a handler's response type opts into binary
// delivery: Blob, *Blob, or a pointer/value of a type defined from Blob (see
// asBlob for the runtime counterpart).
func isBlobResponse(t reflect.Type) bool {
	return isBlobLike(t)
}

// isBlobLike reports whether t is Blob, or a named wrapper that embeds Blob
// as its only field:
//
//	type PreviewFrame struct{ aprot.Blob }
//
// The wrapper exists so a push event can carry raw bytes and still have a
// usable name: a push event's wire name is its Go type name, so registering
// Blob itself would produce an event called "Blob" and allow only one such
// event per registry. The response path accepts the same types, so "a
// top-level Blob is binary" is one rule rather than two.
//
// Embedding is the marker because it cannot happen by accident. Structural
// tests cannot tell an opt-in from a coincidence: Go converts between struct
// types whose underlying types match, and that match ignores struct tags and
// methods, so a consumer's own
//
//	type Attachment struct {
//	    ContentType string `json:"mime"`
//	    Data        []byte `json:"payload"`
//	}
//
// would silently become a Blob — delivered as a binary frame, typed Blob in
// the generated client, its tags and any MarshalJSON ignored. Nobody writes a
// struct whose single field is an embedded aprot.Blob without meaning it.
//
// A wrapper that adds a second field is deliberately not blob-like: it travels
// as ordinary JSON, which is the documented boundary for binary delivery
// (bytes and a content type, nothing else).
func isBlobLike(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == blobType {
		return true
	}
	return blobWrapperField(t) >= 0
}

// blobWrapperField returns the index of the embedded Blob in a named wrapper,
// or -1 when t is not one. One function so the type test and the value
// extraction in asBlob cannot disagree about what a wrapper is.
func blobWrapperField(t reflect.Type) int {
	if t.Kind() != reflect.Struct || t.NumField() != 1 {
		return -1
	}
	if f := t.Field(0); f.Anonymous && f.Type == blobType {
		return 0
	}
	return -1
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
// a binary frame; when a client declines binary and on the byte-stream
// transport it falls back to a JSON
// envelope carrying base64 data under a "$blob" marker. Generated TypeScript
// clients convert both encodings into a DOM Blob, so the client-visible
// result type does not depend on the transport.
//
// Binary delivery applies to Blob as the top-level result of a unary handler
// (including server-driven subscription refreshes), and to the data of a push
// event. A Blob nested inside another struct, streamed as an item, or passed
// as a parameter travels as plain JSON ({contentType, data} with base64
// data).
//
// A push event needs a name, and a push event's wire name is its Go type
// name, so wrap Blob in a named type rather than registering Blob itself:
//
//	type PreviewFrame struct{ aprot.Blob }
//
//	registry.RegisterPushEventFor(&CameraHandlers{}, PreviewFrame{}, aprot.Droppable())
//	server.Broadcast(&PreviewFrame{Blob: aprot.Blob{ContentType: "image/jpeg", Data: jpeg}})
//
// The wrapper must embed Blob and hold no other field. That embedding is the
// opt-in: it is explicit, and it cannot be tripped by a struct that merely
// happens to have a string and a []byte. Such a type is a Blob everywhere
// aprot looks at one, results included, and the generated client types it as a
// DOM Blob. It carries bytes and a content type and nothing else — a push that
// also needs sibling fields (a sequence number, a timestamp) has to travel as
// ordinary JSON.
type Blob struct {
	ContentType string `json:"contentType,omitempty"`
	Data        []byte `json:"data"`
}
