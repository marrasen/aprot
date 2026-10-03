// Package other holds a type whose name collides with one in gogentest, so
// the Go generator's name disambiguation is exercised.
package other

import "database/sql"

// Item has the same name as gogentest.Item.
type Item struct {
	Label string `json:"label"`
}

// WithNull holds a sql.NullString, which the server flattens. The Go
// generator refuses to import such a type through ImportTypes.
type WithNull struct {
	N sql.NullString `json:"n"`
}
