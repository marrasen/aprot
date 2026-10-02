// Package gogentest is the server side of the Go client generator tests. Its
// handlers use a broad mix of types; the generated client for them is
// committed in internal/goclienttest and exercised against a real server by
// the round-trip tests in the root package.
package gogentest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/internal/gentestpkg"
	"github.com/marrasen/aprot/internal/gogentest/other"
)

// Status is a string enum.
type Status string

const (
	StatusPending Status = "pending"
	StatusDone    Status = "done"
	// StatusInProgress has a member name that is not a Go identifier as is.
	StatusInProgress Status = "in-progress"
)

// StatusValues lists every Status.
func StatusValues() []Status { return []Status{StatusPending, StatusDone, StatusInProgress} }

// Priority is an int enum named through fmt.Stringer.
type Priority int

const (
	PriorityLow Priority = iota
	PriorityHigh
)

func (p Priority) String() string {
	if p == PriorityHigh {
		return "High"
	}
	return "Low"
}

// PriorityValues lists every Priority.
func PriorityValues() []Priority { return []Priority{PriorityLow, PriorityHigh} }

// Money marshals itself as a decimal string, so its wire shape is a string.
type Money struct {
	cents int64
}

// NewMoney returns an amount in cents.
func NewMoney(cents int64) Money { return Money{cents: cents} }

func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(fmt.Sprintf("%d.%02d", m.cents/100, m.cents%100))
}

func (m *Money) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		// The zero value of the client's wire type (string).
		m.cents = 0
		return nil
	}
	whole, frac, _ := strings.Cut(s, ".")
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return err
	}
	f, _ := strconv.ParseInt(frac, 10, 64)
	m.cents = w*100 + f
	return nil
}

// Score marshals itself as a JSON number.
type Score int

func (s Score) MarshalJSON() ([]byte, error) { return []byte(strconv.Itoa(int(s))), nil }

// Code marshals through encoding.TextMarshaler, so it is a JSON string.
type Code struct{ v string }

func (c Code) MarshalText() ([]byte, error) { return []byte(c.v), nil }

func (c *Code) UnmarshalText(b []byte) error { c.v = string(b); return nil }

// RawBytes is a named byte slice, which json/v2 encodes as a number array.
type RawBytes []byte

// Base is embedded into Item; its fields are inlined on the wire.
type Base struct {
	ID        int       `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}

// Extra is embedded by pointer.
type Extra struct {
	Note string `json:"note,omitempty"`
}

// inner is an unexported embedded struct; json/v2 still inlines its fields.
type inner struct {
	Secret string `json:"secret"`
}

// Payload is the codegen override for Item.Payload.
type Payload struct {
	Kind string `json:"kind"`
}

// Item exercises every mapping rule of the Go generator.
type Item struct {
	Base
	*Extra
	inner

	// Name is the display name.
	Name     string           `json:"name"`
	Tags     []string         `json:"tags,omitempty"`
	Status   Status           `json:"status"`
	Priority Priority         `json:"priority"`
	Price    Money            `json:"price"`
	Score    Score            `json:"score"`
	Code     Code             `json:"code"`
	Note     sql.NullString   `json:"nullNote"`
	Count    sql.NullInt64    `json:"count"`
	When     sql.NullTime     `json:"when"`
	Gen      sql.Null[int]    `json:"gen"`
	Timeout  time.Duration    `json:"timeout"`
	Interval time.Duration    `json:"interval,format:units"`
	Raw      json.RawMessage  `json:"raw,omitempty"`
	Attrs    map[string]any   `json:"attrs"`
	Matrix   [2][2]int        `json:"matrix"`
	Bytes    []byte           `json:"bytes"`
	Named    RawBytes         `json:"named"`
	Parent   *Item            `json:"parent,omitempty"`
	Children map[string]*Item `json:"children,omitempty"`
	Anon     struct{ X int }  `json:"anon"`
	Big      int64            `json:"big,string"`
	Opt      *string          `json:"opt,omitzero"`
	Payload  any              `json:"payload"`
	File     aprot.Blob       `json:"file"`
	Other    other.Item       `json:"other"`
	Color    gentestpkg.Color `json:"color"`
	ByStatus map[Status]int   `json:"byStatus"`
	Untagged string
	Skipped  string `json:"-"`
	hidden   int
}

// Row is a stream item.
type Row struct {
	N int `json:"n"`
}

// ItemAdded is pushed when an item is added.
type ItemAdded struct {
	ID int `json:"id"`
}

// ErrNotFound is mapped to a custom error code.
var ErrNotFound = errors.New("not found")

// Items is the main handler group.
type Items struct {
	mu     sync.Mutex
	items  []Item
	server *aprot.Server
}

// SetServer lets Add broadcast push events.
func (h *Items) SetServer(s *aprot.Server) { h.server = s }

// Get returns one item. A zero id returns nil without an error.
func (h *Items) Get(ctx context.Context, id int) (*Item, error) {
	if id == 0 {
		return nil, nil
	}
	if id < 0 {
		return nil, ErrNotFound
	}
	it := SampleItem()
	it.ID = id
	return &it, nil
}

// Echo returns its argument, so every field travels both ways.
func (h *Items) Echo(ctx context.Context, item Item) (Item, error) {
	return item, nil
}

// List returns the items with the given status.
func (h *Items) List(ctx context.Context, status Status) ([]Item, error) {
	aprot.RegisterRefreshTrigger(ctx, "items")
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []Item{}
	for _, it := range h.items {
		if it.Status == status {
			out = append(out, it)
		}
	}
	return out, nil
}

// Add stores an item, refreshes List subscriptions and pushes ItemAdded.
func (h *Items) Add(ctx context.Context, item Item) error {
	h.mu.Lock()
	h.items = append(h.items, item)
	h.mu.Unlock()
	aprot.TriggerRefresh(ctx, "items")
	if h.server != nil {
		h.server.Broadcast(ItemAdded{ID: item.ID})
	}
	return nil
}

// Export streams n rows.
func (h *Items) Export(ctx context.Context, n int) (iter.Seq[*Row], error) {
	return func(yield func(*Row) bool) {
		for i := range n {
			if !yield(&Row{N: i}) {
				return
			}
		}
	}, nil
}

// Pairs streams key/value pairs.
func (h *Items) Pairs(ctx context.Context) (iter.Seq2[string, int], error) {
	return func(yield func(string, int) bool) {
		for i, k := range []string{"a", "b", "c"} {
			if !yield(k, i) {
				return
			}
		}
	}, nil
}

// Sum adds its variadic arguments.
func (h *Items) Sum(ctx context.Context, base int, nums ...int) (int, error) {
	for _, n := range nums {
		base += n
	}
	return base, nil
}

// Download returns a binary result.
func (h *Items) Download(ctx context.Context, name string) (*aprot.Blob, error) {
	return &aprot.Blob{ContentType: "text/plain", Data: []byte("hello " + name)}, nil
}

// Admin is a second handler group.
type Admin struct{}

// Ping answers with pong.
func (a *Admin) Ping(ctx context.Context) (string, error) { return "pong", nil }

// NewRegistry builds the registry the fixture client is generated from.
func NewRegistry() (*aprot.Registry, *Items) {
	r := aprot.NewRegistry()
	items := &Items{}
	r.Register(items)
	r.Register(&Admin{})
	r.RegisterEnumFor(items, StatusValues())
	r.RegisterEnum(PriorityValues())
	r.RegisterPushEventFor(items, ItemAdded{})
	r.RegisterError(ErrNotFound, "NotFound")
	r.OverrideFieldType(Item{}, "Payload", Payload{})
	return r, items
}

// ImportTypes is the ImportTypes option the fixture client is generated with.
var ImportTypes = []string{"github.com/marrasen/aprot/internal/gentestpkg"}

// SampleItem returns an Item with every field set.
func SampleItem() Item {
	opt := "optional"
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return Item{
		Base:     Base{ID: 1, CreatedAt: when},
		Extra:    &Extra{Note: "extra"},
		inner:    inner{Secret: "s3cret"},
		Name:     "first",
		Tags:     []string{"a", "b"},
		Status:   StatusInProgress,
		Priority: PriorityHigh,
		Price:    NewMoney(1234),
		Score:    7,
		Code:     Code{v: "XYZ"},
		Note:     sql.NullString{String: "note", Valid: true},
		Count:    sql.NullInt64{},
		When:     sql.NullTime{Time: when, Valid: true},
		Gen:      sql.Null[int]{V: 5, Valid: true},
		Timeout:  1500 * time.Millisecond,
		Interval: 90 * time.Second,
		Raw:      json.RawMessage(`{"k":[1,2]}`),
		Attrs:    map[string]any{"n": 1.5, "s": "x"},
		Matrix:   [2][2]int{{1, 2}, {3, 4}},
		Bytes:    []byte("bytes"),
		Named:    RawBytes{1, 2, 3},
		Parent:   &Item{Name: "parent", Attrs: map[string]any{}},
		Children: map[string]*Item{"c": {Name: "child"}},
		Anon:     struct{ X int }{X: 9},
		Big:      1 << 60,
		Opt:      &opt,
		Payload:  Payload{Kind: "k"},
		File:     aprot.Blob{ContentType: "text/plain", Data: []byte("blob")},
		Other:    other.Item{Label: "other"},
		Color:    gentestpkg.ColorGreen,
		ByStatus: map[Status]int{StatusDone: 2},
		Untagged: "untagged",
		Skipped:  "skipped",
		hidden:   1,
	}
}
