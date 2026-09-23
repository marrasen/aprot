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

// isBlobLike reports whether t is Blob, or a distinct type whose underlying
// type is Blob's:
//
//	type PreviewFrame aprot.Blob
//
// A defined type exists so a push event can carry raw bytes and still have a
// usable name: a push event's wire name is its Go type name, so registering
// Blob itself would produce an event called "Blob" and allow only one such
// event per registry. The response path accepts the same types, so "a
// top-level Blob is binary" is one rule rather than two.
//
// Convertibility is the exact test wanted here: for struct types Go allows a
// conversion only when the underlying types are identical (field names, types
// and order; tags are ignored). A struct that passes is structurally a Blob,
// so treating it as one is right rather than a coincidence.
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
	return t.Kind() == reflect.Struct && t.ConvertibleTo(blobType)
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
// name, so define a type from Blob rather than registering Blob itself:
//
//	type PreviewFrame aprot.Blob
//
//	registry.RegisterPushEventFor(&CameraHandlers{}, PreviewFrame{}, aprot.Droppable())
//	server.Broadcast(&PreviewFrame{ContentType: "image/jpeg", Data: jpeg})
//
// Such a type is a Blob everywhere aprot looks at one, results included, and
// the generated client types it as a DOM Blob. It carries bytes and a content
// type and nothing else — a push that also needs sibling fields (a sequence
// number, a timestamp) has to travel as ordinary JSON.
type Blob struct {
	ContentType string `json:"contentType,omitempty"`
	Data        []byte `json:"data"`
}
