package aprot

import (
	"database/sql"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
)

func TestMarshalJSON_SQLNull(t *testing.T) {
	type Response struct {
		Name  sql.NullString  `json:"name"`
		Age   sql.NullInt64   `json:"age"`
		Score sql.NullFloat64 `json:"score"`
		OK    sql.NullBool    `json:"ok"`
		Rank  sql.NullInt32   `json:"rank"`
		Level sql.NullInt16   `json:"level"`
		Code  sql.NullByte    `json:"code"`
		Born  sql.NullTime    `json:"born"`
	}

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		resp Response
		want string
	}{
		{
			name: "all valid",
			resp: Response{
				Name:  sql.NullString{String: "Alice", Valid: true},
				Age:   sql.NullInt64{Int64: 30, Valid: true},
				Score: sql.NullFloat64{Float64: 9.5, Valid: true},
				OK:    sql.NullBool{Bool: true, Valid: true},
				Rank:  sql.NullInt32{Int32: 1, Valid: true},
				Level: sql.NullInt16{Int16: 5, Valid: true},
				Code:  sql.NullByte{Byte: 42, Valid: true},
				Born:  sql.NullTime{Time: now, Valid: true},
			},
			want: `{"name":"Alice","age":30,"score":9.5,"ok":true,"rank":1,"level":5,"code":42,"born":"2025-06-15T12:00:00Z"}`,
		},
		{
			name: "all null",
			resp: Response{},
			want: `{"name":null,"age":null,"score":null,"ok":null,"rank":null,"level":null,"code":null,"born":null}`,
		},
		{
			name: "mixed",
			resp: Response{
				Name: sql.NullString{String: "Bob", Valid: true},
				Age:  sql.NullInt64{Valid: false},
				Born: sql.NullTime{Time: now, Valid: true},
			},
			want: `{"name":"Bob","age":null,"score":null,"ok":null,"rank":null,"level":null,"code":null,"born":"2025-06-15T12:00:00Z"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := marshalJSON(tt.resp)
			if err != nil {
				t.Fatalf("marshalJSON error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("marshalJSON =\n  %s\nwant\n  %s", got, tt.want)
			}
		})
	}
}

func TestUnmarshalJSON_SQLNull(t *testing.T) {
	type Request struct {
		Name  sql.NullString  `json:"name"`
		Age   sql.NullInt64   `json:"age"`
		Score sql.NullFloat64 `json:"score"`
		OK    sql.NullBool    `json:"ok"`
		Rank  sql.NullInt32   `json:"rank"`
		Level sql.NullInt16   `json:"level"`
		Code  sql.NullByte    `json:"code"`
		Born  sql.NullTime    `json:"born"`
	}

	t.Run("values", func(t *testing.T) {
		var req Request
		err := unmarshalJSON([]byte(`{"name":"Alice","age":30,"score":9.5,"ok":true,"rank":1,"level":5,"code":42,"born":"2025-06-15T12:00:00Z"}`), &req)
		if err != nil {
			t.Fatalf("unmarshalJSON error: %v", err)
		}
		if !req.Name.Valid || req.Name.String != "Alice" {
			t.Errorf("Name = %+v, want {Alice true}", req.Name)
		}
		if !req.Age.Valid || req.Age.Int64 != 30 {
			t.Errorf("Age = %+v, want {30 true}", req.Age)
		}
		if !req.Score.Valid || req.Score.Float64 != 9.5 {
			t.Errorf("Score = %+v, want {9.5 true}", req.Score)
		}
		if !req.OK.Valid || !req.OK.Bool {
			t.Errorf("OK = %+v, want {true true}", req.OK)
		}
		if !req.Rank.Valid || req.Rank.Int32 != 1 {
			t.Errorf("Rank = %+v, want {1 true}", req.Rank)
		}
		if !req.Level.Valid || req.Level.Int16 != 5 {
			t.Errorf("Level = %+v, want {5 true}", req.Level)
		}
		if !req.Code.Valid || req.Code.Byte != 42 {
			t.Errorf("Code = %+v, want {42 true}", req.Code)
		}
		if !req.Born.Valid {
			t.Errorf("Born = %+v, want Valid=true", req.Born)
		}
	})

	t.Run("nulls", func(t *testing.T) {
		var req Request
		err := unmarshalJSON([]byte(`{"name":null,"age":null,"score":null,"ok":null,"rank":null,"level":null,"code":null,"born":null}`), &req)
		if err != nil {
			t.Fatalf("unmarshalJSON error: %v", err)
		}
		if req.Name.Valid {
			t.Errorf("Name should be invalid, got %+v", req.Name)
		}
		if req.Age.Valid {
			t.Errorf("Age should be invalid, got %+v", req.Age)
		}
		if req.OK.Valid {
			t.Errorf("OK should be invalid, got %+v", req.OK)
		}
		if req.Born.Valid {
			t.Errorf("Born should be invalid, got %+v", req.Born)
		}
	})
}

func TestMarshalJSON_RoundTrip(t *testing.T) {
	// Verify that marshalJSON output can be round-tripped through unmarshalJSON.
	type Data struct {
		Name sql.NullString `json:"name"`
		Age  sql.NullInt64  `json:"age"`
	}

	original := Data{
		Name: sql.NullString{String: "test", Valid: true},
		Age:  sql.NullInt64{Valid: false},
	}

	b, err := marshalJSON(original)
	if err != nil {
		t.Fatal(err)
	}

	var decoded Data
	if err := unmarshalJSON(b, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.Name != original.Name {
		t.Errorf("Name = %+v, want %+v", decoded.Name, original.Name)
	}
	if decoded.Age != original.Age {
		t.Errorf("Age = %+v, want %+v", decoded.Age, original.Age)
	}
}

func TestMarshalJSON_NonSQLTypes(t *testing.T) {
	// Verify that non-sql types still marshal normally.
	type Plain struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	b, err := marshalJSON(Plain{Name: "Alice", Age: 30})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"name":"Alice","age":30}` {
		t.Errorf("got %s", b)
	}
}

func TestMarshalJSON_WithoutOptions(t *testing.T) {
	// Confirm the bug: without our options, sql.Null* types marshal as structs.
	s := sql.NullString{String: "hello", Valid: true}
	b, _ := json.Marshal(s)
	if string(b) == `"hello"` {
		t.Fatal("sql.NullString now marshals correctly without custom options — custom marshalers may no longer be needed")
	}
}
