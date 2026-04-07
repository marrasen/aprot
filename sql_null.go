package aprot

import (
	"database/sql"

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
)

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
)

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
