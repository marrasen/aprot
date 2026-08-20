package aprot

import (
	"database/sql"
	"encoding/json/v2"
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
