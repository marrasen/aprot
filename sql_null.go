package aprot

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"time"

	experimentjson "github.com/go-json-experiment/json"
)

// sqlNullMarshalers provides custom JSON marshalers for database/sql nullable
// types. The sql.Null* types do not implement json.Marshaler, so without these
// overrides they serialize as {"String":"...","Valid":true} instead of the
// unwrapped value or null.
var sqlNullMarshalers = json.JoinMarshalers(
	json.MarshalFunc(func(v sql.NullString) ([]byte, error) {
		if !v.Valid {
			return []byte("null"), nil
		}
		return json.Marshal(v.String)
	}),
	json.MarshalFunc(func(v sql.NullInt64) ([]byte, error) {
		if !v.Valid {
			return []byte("null"), nil
		}
		return json.Marshal(v.Int64)
	}),
	json.MarshalFunc(func(v sql.NullInt32) ([]byte, error) {
		if !v.Valid {
			return []byte("null"), nil
		}
		return json.Marshal(v.Int32)
	}),
	json.MarshalFunc(func(v sql.NullInt16) ([]byte, error) {
		if !v.Valid {
			return []byte("null"), nil
		}
		return json.Marshal(v.Int16)
	}),
	json.MarshalFunc(func(v sql.NullFloat64) ([]byte, error) {
		if !v.Valid {
			return []byte("null"), nil
		}
		return json.Marshal(v.Float64)
	}),
	json.MarshalFunc(func(v sql.NullBool) ([]byte, error) {
		if !v.Valid {
			return []byte("null"), nil
		}
		return json.Marshal(v.Bool)
	}),
	json.MarshalFunc(func(v sql.NullByte) ([]byte, error) {
		if !v.Valid {
			return []byte("null"), nil
		}
		return json.Marshal(v.Byte)
	}),
	json.MarshalFunc(func(v sql.NullTime) ([]byte, error) {
		if !v.Valid {
			return []byte("null"), nil
		}
		return json.Marshal(v.Time)
	}),
	// Generic sql.Null[T] (Go 1.22+). A marshaler can only be registered for a
	// concrete instantiation, so cover the common T; these unwrap to value-or-
	// null to match the generated `T | null` codegen. Exotic instantiations
	// (e.g. Null[CustomStruct]) fall back to the default {"V":…,"Valid":…}.
	marshalGenericNull[string](),
	marshalGenericNull[int](),
	marshalGenericNull[int64](),
	marshalGenericNull[int32](),
	marshalGenericNull[int16](),
	marshalGenericNull[float64](),
	marshalGenericNull[bool](),
	marshalGenericNull[time.Time](),
)

// marshalGenericNull builds a marshaler for sql.Null[T] that emits the unwrapped
// value when Valid and null otherwise.
func marshalGenericNull[T any]() *json.Marshalers {
	return json.MarshalFunc(func(v sql.Null[T]) ([]byte, error) {
		if !v.Valid {
			return []byte("null"), nil
		}
		return json.Marshal(v.V)
	})
}

// sqlNullUnmarshalers provides custom JSON unmarshalers for database/sql
// nullable types. Since sql.Null* types do not implement json.Unmarshaler,
// this allows clients to send unwrapped values (e.g. "hello" instead of
// {"String":"hello","Valid":true}) and have them correctly deserialized.
var sqlNullUnmarshalers = json.JoinUnmarshalers(
	json.UnmarshalFunc(func(data []byte, v *sql.NullString) error {
		if string(data) == "null" {
			v.Valid = false
			return nil
		}
		v.Valid = true
		return json.Unmarshal(data, &v.String)
	}),
	json.UnmarshalFunc(func(data []byte, v *sql.NullInt64) error {
		if string(data) == "null" {
			v.Valid = false
			return nil
		}
		v.Valid = true
		return json.Unmarshal(data, &v.Int64)
	}),
	json.UnmarshalFunc(func(data []byte, v *sql.NullInt32) error {
		if string(data) == "null" {
			v.Valid = false
			return nil
		}
		v.Valid = true
		return json.Unmarshal(data, &v.Int32)
	}),
	json.UnmarshalFunc(func(data []byte, v *sql.NullInt16) error {
		if string(data) == "null" {
			v.Valid = false
			return nil
		}
		v.Valid = true
		return json.Unmarshal(data, &v.Int16)
	}),
	json.UnmarshalFunc(func(data []byte, v *sql.NullFloat64) error {
		if string(data) == "null" {
			v.Valid = false
			return nil
		}
		v.Valid = true
		return json.Unmarshal(data, &v.Float64)
	}),
	json.UnmarshalFunc(func(data []byte, v *sql.NullBool) error {
		if string(data) == "null" {
			v.Valid = false
			return nil
		}
		v.Valid = true
		return json.Unmarshal(data, &v.Bool)
	}),
	json.UnmarshalFunc(func(data []byte, v *sql.NullByte) error {
		if string(data) == "null" {
			v.Valid = false
			return nil
		}
		v.Valid = true
		return json.Unmarshal(data, &v.Byte)
	}),
	json.UnmarshalFunc(func(data []byte, v *sql.NullTime) error {
		if string(data) == "null" {
			v.Valid = false
			return nil
		}
		v.Valid = true
		return json.Unmarshal(data, &v.Time)
	}),
	// Generic sql.Null[T] (Go 1.22+) for the common instantiations — accept the
	// unwrapped value (or null), parallel to the concrete types above.
	unmarshalGenericNull[string](),
	unmarshalGenericNull[int](),
	unmarshalGenericNull[int64](),
	unmarshalGenericNull[int32](),
	unmarshalGenericNull[int16](),
	unmarshalGenericNull[float64](),
	unmarshalGenericNull[bool](),
	unmarshalGenericNull[time.Time](),
)

// unmarshalGenericNull builds an unmarshaler for sql.Null[T] that accepts the
// unwrapped value or null.
func unmarshalGenericNull[T any]() *json.Unmarshalers {
	return json.UnmarshalFunc(func(data []byte, v *sql.Null[T]) error {
		if string(data) == "null" {
			v.Valid = false
			return nil
		}
		v.Valid = true
		return json.Unmarshal(data, &v.V)
	})
}

// sqlNullOptions combines the marshal and unmarshal overrides for sql.Null*
// types into a single Options value ready for json.Marshal / json.Unmarshal.
var sqlNullOptions = json.JoinOptions(
	json.WithMarshalers(sqlNullMarshalers),
	json.WithUnmarshalers(sqlNullUnmarshalers),
)

// wireJSONOptions is the single options set applied everywhere aprot marshals
// or unmarshals user data: response results, request params, push/refresh
// payloads, stream items, the $blob JSON fallback, and the codegen's
// zero-value marshaling probes. It combines the sql.Null* overrides with the
// opt-in for per-field `format:` struct tags: json/v2 rejects format-tagged
// fields at marshal/unmarshal time unless this option is set, and aprot's
// codegen requires such tags on some types (`json:"d,format:nano"` on
// time.Duration, and the byte-slice shape overrides of #240).
//
// The option comes from github.com/go-json-experiment/json even though
// everything else here is encoding/json/v2. The `format:` tag stayed
// experimental when json/v2 landed in Go 1.27: encoding/json/v2 exports no
// constructor for it, and the standard library reaches this feature through a
// duck-typed interface it documents by name as being for
// [github.com/go-json-experiment/json.ExperimentalSupportFormatTag]
// (see encoding/json/internal/jsonopts.experimentalFormatTagSupporter). The
// option is passed to the stdlib marshaler and takes effect there; the
// staging module is not doing the encoding. Drop this import when the
// standard library exports its own opt-in (#344).
var wireJSONOptions = json.JoinOptions(
	sqlNullOptions,
	experimentjson.ExperimentalSupportFormatTag(true),
)

// formatTagCanary is the probe checkFormatTagSupport marshals. The `format:`
// tag is the whole point: without the opt-in applying, json/v2 rejects the
// field outright rather than falling back to a default encoding.
type formatTagCanary struct {
	D time.Duration `json:"d,format:nano"`
}

// formatTagCanaryWant is what one second must encode to when the opt-in
// applies.
const formatTagCanaryWant = `{"d":1000000000}`

// checkFormatTagSupport verifies that the format-tag opt-in in
// [wireJSONOptions] actually reaches the standard library's marshaler.
//
// It exists because that opt-in crosses a duck-typed seam (see
// wireJSONOptions): aprot passes an option value from
// github.com/go-json-experiment/json, and encoding/json/v2 recognizes it
// through an unexported interface. aprot's own CI pins one version of that
// module, but a consumer's build resolves whatever version MVS picks across
// their whole module graph. If that version's option stops satisfying the
// stdlib's marker interface — or a future Go release changes the interface —
// the opt-in silently stops applying in that consumer's build alone, and
// every `format:` tag aprot's codegen relies on (`format:nano` durations,
// the byte-slice shape overrides of #240) changes behavior on the wire.
// aprot's tests cannot see that; the consumer sees client-side decode
// errors.
//
// Checking it once at init turns that silent, build-specific wire drift into
// a loud startup failure. Remove this together with the staging dependency
// when the standard library exports its own opt-in (#344).
func checkFormatTagSupport() error {
	out, err := json.Marshal(formatTagCanary{D: time.Second}, wireJSONOptions)
	if err != nil {
		return fmt.Errorf("marshaling a `format:` tagged field failed: %w", err)
	}
	if string(out) != formatTagCanaryWant {
		return fmt.Errorf("a `format:` tagged field encoded as %s, want %s", out, formatTagCanaryWant)
	}
	return nil
}

func init() {
	if err := checkFormatTagSupport(); err != nil {
		panic("aprot: the json/v2 `format:` struct tag opt-in is not taking effect: " + err.Error() +
			"\n\naprot passes github.com/go-json-experiment/json.ExperimentalSupportFormatTag to the" +
			" standard library's encoding/json/v2, which accepts it through an unexported interface." +
			" A version of that module resolved by your module graph no longer satisfies that" +
			" interface, or your Go toolchain changed it." +
			"\n\nFix: align github.com/go-json-experiment/json with the version aprot requires" +
			" (`go get github.com/go-json-experiment/json@$(go list -m -f '{{.Version}}'" +
			" github.com/go-json-experiment/json)` after upgrading aprot), or upgrade aprot." +
			" Without this option every `format:` struct tag is rejected, which would change" +
			" aprot's wire format for time.Duration and byte-slice fields.")
	}
}

// marshalJSON marshals v to JSON with aprot's wire semantics: sql.Null* type
// support and `format:` struct tag support.
func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v, wireJSONOptions)
}

// MarshalWire marshals v with aprot's wire semantics — sql.Null* flattening
// and `format:` struct tag support — the exact encoding every transport uses
// for responses. Adapters outside the aprot package (aprot/mcp, custom
// transports built on [Server.Invoke]) use it so their payloads match the
// WebSocket/SSE/REST wire format.
func MarshalWire(v any) ([]byte, error) {
	return marshalJSON(v)
}

// unmarshalJSON unmarshals data into v with aprot's wire semantics: sql.Null*
// type support and `format:` struct tag support.
func unmarshalJSON(data []byte, v any) error {
	return json.Unmarshal(data, v, wireJSONOptions)
}
