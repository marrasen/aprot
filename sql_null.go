package aprot

import (
	"database/sql"
	"time"

	"github.com/go-json-experiment/json"
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

// marshalJSON marshals v to JSON with sql.Null* type support.
func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v, sqlNullOptions)
}

// unmarshalJSON unmarshals data into v with sql.Null* type support.
func unmarshalJSON(data []byte, v any) error {
	return json.Unmarshal(data, v, sqlNullOptions)
}
