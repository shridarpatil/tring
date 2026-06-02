package queries

import (
	"reflect"
	"strings"
	"testing"
)

// TestLoadResolvesEveryTag is the boot-time guard: every `query:"..."` field
// must bind to a `-- name:` block in queries.sql. MustLoad would panic on a
// mismatch, so a green test here means the catalog and struct are in sync.
func TestLoadResolvesEveryTag(t *testing.T) {
	q, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	v := reflect.ValueOf(q).Elem()
	tp := v.Type()
	for i := 0; i < tp.NumField(); i++ {
		tag := tp.Field(i).Tag.Get("query")
		if tag == "" {
			continue
		}
		if strings.TrimSpace(v.Field(i).String()) == "" {
			t.Errorf("field %s (query:%q) resolved to empty SQL", tp.Field(i).Name, tag)
		}
	}
}

// TestParseKeepsBodyComments confirms the parser keeps a query's own `--`
// comment lines in its body (pgx ignores them) while still splitting on the
// `-- name:` headers.
func TestParseKeepsBodyComments(t *testing.T) {
	m := parse([]byte("-- name: a\n-- a note\nSELECT 1;\n-- name: b\nSELECT 2;\n"))
	if got := m["a"]; got != "-- a note\nSELECT 1;" {
		t.Errorf("query a = %q, want body with comment preserved", got)
	}
	if got := m["b"]; got != "SELECT 2;" {
		t.Errorf("query b = %q", got)
	}
}
