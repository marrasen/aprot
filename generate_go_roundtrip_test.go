package aprot_test

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	experimentjson "github.com/go-json-experiment/json"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/client"
	"github.com/marrasen/aprot/internal/gentestpkg"
	"github.com/marrasen/aprot/internal/goclienttest"
	"github.com/marrasen/aprot/internal/gogentest"
)

// These tests call a real server through the committed generated client in
// internal/goclienttest (kept fresh by TestGoClientFixtureUpToDate).

func dialGoClientFixture(t *testing.T) (*goclienttest.Client, *gogentest.Items) {
	t.Helper()
	registry, items := gogentest.NewRegistry()
	server := aprot.NewServer(registry)
	items.SetServer(server)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), client.Options{NoReconnect: true})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return goclienttest.New(c), items
}

// clientWireOptions are the options the runtime client decodes with.
var clientWireOptions = json.JoinOptions(
	jsonv1.FormatDurationAsNano(true),
	experimentjson.ExperimentalSupportFormatTag(true),
)

// normalizeJSON decodes data into a generic value so two encodings compare
// regardless of key order.
func normalizeJSON(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decoding %s: %v", data, err)
	}
	return v
}

// assertSameWire fails unless the generated client value re-encodes to the
// same JSON the server produced for want: the generated type then reproduces
// the wire shape field for field.
func assertSameWire(t *testing.T, serverValue any, clientValue any) {
	t.Helper()
	want, err := aprot.MarshalWire(serverValue)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(clientValue, clientWireOptions)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalizeJSON(t, want), normalizeJSON(t, got)) {
		t.Errorf("wire shape differs\nserver: %s\nclient: %s", want, got)
	}
}

func TestGoClientRoundTripUnary(t *testing.T) {
	api, _ := dialGoClientFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := api.Items.Get(ctx, 5)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	want := gogentest.SampleItem()
	want.ID = 5
	assertSameWire(t, want, got)

	// Spot-check the mapped fields by their Go values.
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	checks := []struct {
		name      string
		got, want any
	}{
		{"ID (embedded Base)", got.ID, 5},
		{"CreatedAt", got.CreatedAt.Equal(when), true},
		{"Extra.Note (embedded pointer)", got.Extra != nil && got.Extra.Note == "extra", true},
		{"Secret (unexported embedded)", got.Secret, "s3cret"},
		{"Status enum", got.Status, goclienttest.StatusInProgress},
		{"Priority enum", got.Priority, goclienttest.PriorityHigh},
		{"Price (MarshalJSON string)", got.Price, "12.34"},
		{"Score (MarshalJSON number, raw JSON)", string(got.Score), "7"},
		{"Code (MarshalText)", got.Code, "XYZ"},
		{"NullString", got.Note != nil && *got.Note == "note", true},
		{"NullInt64 invalid", got.Count == nil, true},
		{"NullTime", got.When != nil && got.When.Equal(when), true},
		{"Null[int]", got.Gen != nil && *got.Gen == 5, true},
		{"Duration", got.Timeout, 1500 * time.Millisecond},
		{"Duration format:units", got.Interval, 90 * time.Second},
		{"Raw", string(got.Raw), `{"k":[1,2]}`},
		{"Matrix", got.Matrix, [2][2]int{{1, 2}, {3, 4}}},
		{"Bytes", string(got.Bytes), "bytes"},
		{"Named bytes", got.Named, goclienttest.RawBytes{1, 2, 3}},
		{"Parent", got.Parent != nil && got.Parent.Name == "parent", true},
		{"Children", got.Children["c"] != nil && got.Children["c"].Name == "child", true},
		{"Anon", got.Anon.X, 9},
		{"Big (string option)", got.Big, int64(1 << 60)},
		{"Opt", got.Opt != nil && *got.Opt == "optional", true},
		{"Payload (override)", got.Payload, &goclienttest.Payload{Kind: "k"}},
		{"Blob nested", got.File, client.Blob{ContentType: "text/plain", Data: []byte("blob")}},
		{"Other (disambiguated)", got.Other, goclienttest.OtherItem{Label: "other"}},
		{"Color (ImportTypes)", got.Color, gentestpkg.ColorGreen},
		{"ByStatus", got.ByStatus, map[goclienttest.Status]int{goclienttest.StatusDone: 2}},
		{"Untagged", got.Untagged, "untagged"},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: got %#v, want %#v", c.name, c.got, c.want)
		}
	}

	// Echo sends the generated type to the server and back.
	echoed, err := api.Items.Echo(ctx, *got)
	if err != nil {
		t.Fatalf("Echo: %v", err)
	}
	assertSameWire(t, want, echoed)

	// A nil *T result stays nil.
	none, err := api.Items.Get(ctx, 0)
	if err != nil || none != nil {
		t.Errorf("Get(0) = %v, %v; want nil, nil", none, err)
	}

	// Registered errors carry their generated code.
	if _, err := api.Items.Get(ctx, -1); !client.HasCode(err, goclienttest.ErrCodeNotFound) {
		t.Errorf("Get(-1) error = %v, want code ErrCodeNotFound", err)
	}

	// Variadic parameters are spread into positional params.
	if sum, err := api.Items.Sum(ctx, 1, 2, 3); err != nil || sum != 6 {
		t.Errorf("Sum = %d, %v; want 6", sum, err)
	}

	// A top-level Blob arrives as a binary frame.
	blob, err := api.Items.Download(ctx, "x")
	if err != nil || blob == nil || string(blob.Data) != "hello x" || blob.ContentType != "text/plain" {
		t.Errorf("Download = %+v, %v", blob, err)
	}

	if pong, err := api.Admin.Ping(ctx); err != nil || pong != "pong" {
		t.Errorf("Ping = %q, %v", pong, err)
	}
}

func TestGoClientRoundTripSubscriptionAndPush(t *testing.T) {
	api, _ := dialGoClientFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pushed := make(chan goclienttest.ItemAdded, 1)
	remove := api.OnItemAdded(func(ev goclienttest.ItemAdded) { pushed <- ev })
	defer remove()

	// client.OnError needs no type argument on a generated Subscribe method.
	sub := api.Items.SubscribeList(ctx, goclienttest.StatusDone, client.OnError(func(err error) {
		t.Errorf("refresh error: %v", err)
	}))
	defer sub.Close()

	select {
	case first, ok := <-sub.C:
		if !ok {
			t.Fatalf("subscription closed: %v", sub.Err())
		}
		if len(first) != 0 {
			t.Fatalf("first result = %v, want empty", first)
		}
	case <-ctx.Done():
		t.Fatal("no initial subscription result")
	}

	item := goclienttest.GogentestItem{Name: "added", Status: goclienttest.StatusDone}
	item.ID = 42
	if err := api.Items.Add(ctx, item); err != nil {
		t.Fatalf("Add: %v", err)
	}

	select {
	case refreshed, ok := <-sub.C:
		if !ok {
			t.Fatalf("subscription closed: %v", sub.Err())
		}
		if len(refreshed) != 1 || refreshed[0].ID != 42 || refreshed[0].Name != "added" {
			t.Fatalf("refreshed result = %+v, want the added item", refreshed)
		}
	case <-ctx.Done():
		t.Fatal("no refresh after TriggerRefresh")
	}

	select {
	case ev := <-pushed:
		if ev.ID != 42 {
			t.Errorf("push ID = %d, want 42", ev.ID)
		}
	case <-ctx.Done():
		t.Fatal("no push event")
	}
}

func TestGoClientRoundTripStreams(t *testing.T) {
	api, _ := dialGoClientFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows := api.Items.Export(ctx, 3)
	defer rows.Close()
	var ns []int
	for r := range rows.All() {
		ns = append(ns, r.N)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !reflect.DeepEqual(ns, []int{0, 1, 2}) {
		t.Errorf("Export rows = %v", ns)
	}

	pairs := api.Items.Pairs(ctx)
	defer pairs.Close()
	got := map[string]int{}
	for k, v := range pairs.All() {
		got[k] = v
	}
	if err := pairs.Err(); err != nil {
		t.Fatalf("Pairs: %v", err)
	}
	if !reflect.DeepEqual(got, map[string]int{"a": 0, "b": 1, "c": 2}) {
		t.Errorf("Pairs = %v", got)
	}
}
