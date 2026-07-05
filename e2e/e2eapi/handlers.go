// Package e2eapi adds REST and validation handlers to the e2e server so the
// REST adapter (typed scalar path params and sql.Null response marshaling) and
// the validation -> getValidationErrors round-trip get end-to-end coverage.
// These surfaces had no e2e tests, which is how the typed-path-param REST bug
// (#207 P1) survived.
package e2eapi

import (
	"context"
	"database/sql"
	"sync"

	"github.com/marrasen/aprot"
)

// EchoHandlers is exposed over REST to exercise typed scalar path params and
// sql.Null response marshaling.
type EchoHandlers struct{}

// EchoResult is the REST response. Note is a sql.NullString so the test can
// confirm the REST transport unwraps it to a bare string / null (matching
// WS/SSE), not the {"String":…,"Valid":…} object.
type EchoResult struct {
	Count int            `json:"count"`
	Flag  bool           `json:"flag"`
	Label string         `json:"label"`
	Note  sql.NullString `json:"note"`
}

// GetEcho echoes its typed path params. Before #207 P1, the int and bool path
// params returned 400 InvalidParams over REST because the raw path string did
// not decode into the Go scalar.
func (h *EchoHandlers) GetEcho(ctx context.Context, count int, flag bool, label string) (*EchoResult, error) {
	note := sql.NullString{}
	if label != "" {
		note = sql.NullString{String: "hi-" + label, Valid: true}
	}
	return &EchoResult{Count: count, Flag: flag, Label: label, Note: note}, nil
}

// SignupHandlers is registered over WebSocket to exercise the validation ->
// getValidationErrors round-trip from the generated client.
type SignupHandlers struct{}

// SignupRequest carries validate tags spanning several rule kinds so the
// structured FieldError payload has multiple entries to assert on.
type SignupRequest struct {
	Name  string `json:"name"  validate:"required,min=2,max=20"`
	Email string `json:"email" validate:"required,email"`
	Age   int    `json:"age"   validate:"gte=13,lte=120"`
}

// SignupResult is the success payload.
type SignupResult struct {
	OK bool `json:"ok"`
}

// Signup succeeds for any input that passes validation.
func (h *SignupHandlers) Signup(ctx context.Context, req *SignupRequest) (*SignupResult, error) {
	return &SignupResult{OK: true}, nil
}

// FixedArrayHandlers exercises fixed-size array codegen (#240): [N]T fields
// must be typed as TS tuples (plain arrays above the tuple cap) and round-trip
// as JSON arrays, while [N]byte crosses the wire as a base64 string.
type FixedArrayHandlers struct{}

// FixedArrayPayload covers the [N]T shapes with distinct wire encodings:
// primitive tuple, nested tuple, above-cap fallback, and base64 byte array.
type FixedArrayPayload struct {
	WBMul [4]float64 `json:"wbMul"`
	Grid  [2][2]int  `json:"grid"`
	Big   [20]int    `json:"big"`
	Hash  [8]byte    `json:"hash"`
}

// EchoArrays echoes its payload so the test can assert a full round-trip.
func (h *FixedArrayHandlers) EchoArrays(ctx context.Context, req *FixedArrayPayload) (*FixedArrayPayload, error) {
	return req, nil
}

// BlobHandlers exercises binary Blob responses (#238). A top-level Blob
// result crosses the wire as a WebSocket binary frame, or as the $blob JSON
// fallback on transports without binary frames (SSE); generated clients must
// resolve a DOM Blob either way — including for subscription refreshes.
type BlobHandlers struct {
	mu   sync.Mutex
	data []byte
}

// NewBlobHandlers seeds a payload with non-UTF-8 bytes so the tests catch
// any encoding corruption on either the binary or the base64 fallback path.
func NewBlobHandlers() *BlobHandlers {
	return &BlobHandlers{data: []byte{0x00, 0x01, 0xfe, 0xff, 'v', '1'}}
}

// GetBlob returns the current payload and subscribes callers to SetBlob
// refreshes via the "blob" trigger key.
func (h *BlobHandlers) GetBlob(ctx context.Context) (aprot.Blob, error) {
	aprot.RegisterRefreshTrigger(ctx, "blob")
	h.mu.Lock()
	defer h.mu.Unlock()
	return aprot.Blob{
		ContentType: "application/x-e2e",
		Data:        append([]byte(nil), h.data...),
	}, nil
}

// SetBlob replaces the payload and refreshes every GetBlob subscriber.
func (h *BlobHandlers) SetBlob(ctx context.Context, data string) error {
	h.mu.Lock()
	h.data = []byte(data)
	h.mu.Unlock()
	aprot.TriggerRefresh(ctx, "blob")
	return nil
}

// Register wires the e2e-only REST and validation handlers onto an existing
// registry and turns on request validation. Existing handlers without validate
// tags are unaffected (validation is a no-op for them).
func Register(registry *aprot.Registry) {
	registry.RegisterREST(&EchoHandlers{})
	registry.Register(&SignupHandlers{})
	registry.Register(&FixedArrayHandlers{})
	registry.Register(NewBlobHandlers())
	registry.SetValidator(aprot.NewPlaygroundValidator())
}
