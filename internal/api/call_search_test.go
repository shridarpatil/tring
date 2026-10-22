package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

func TestCallSearch(t *testing.T) {
	cases := []struct{ in, id, digits string }{
		{"", "", ""},
		{"9800000001", "9800000001", "9800000001"}, // a number, or an all-digit id prefix
		{"+91 98000-00001", "", "919800000001"},
		{"09800000001", "09800000001", "9800000001"}, // national leading 0 dropped
		{"9800", "", "9800"},                         // too short to be an id prefix
		{"15693408-4339", "15693408-4339", "156934084339"},
		{"3F2B9A1C", "3f2b9a1c", ""}, // call id prefix, case-insensitive
		{"3f2b9a1c-0000-4000", "3f2b9a1c-0000-4000", ""},
		{"ab%_cd", "abcd", ""}, // no LIKE wildcards from the user
	}
	for _, c := range cases {
		id, digits := callSearch(c.in)
		if id != c.id || digits != c.digits {
			t.Errorf("callSearch(%q) = (%q, %q), want (%q, %q)", c.in, id, digits, c.id, c.digits)
		}
	}
}

func TestListCallsSearch(t *testing.T) {
	f := dbtest.Setup(t)
	ctx := context.Background()
	var phoneCall, otherCall string
	if err := f.DB.Pool.QueryRow(ctx, `INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id)
		VALUES ($1, 'inbound', 'ended', '+919800000001', '+919800000089', 'leg-a') RETURNING id`, f.TenantID).Scan(&phoneCall); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, `INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id)
		VALUES ($1, 'outbound', 'ringing', '919811111111', '+919822222222', 'leg-b') RETURNING id`, f.TenantID).Scan(&otherCall); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestServer(f)
	list := func(query string) (ids []string, total int, code int) {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, authedRequest(t, s, f, "GET", "/v1/calls?page=1"+query, ""))
		var env struct {
			Items []struct{ ID string } `json:"items"`
			Total int                   `json:"total"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		for _, it := range env.Items {
			ids = append(ids, it.ID)
		}
		return ids, env.Total, rec.Code
	}

	cases := []struct {
		name, query string
		want        []string
	}{
		{"all", "", []string{otherCall, phoneCall}},
		{"bare 10-digit number", "&q=9800000001", []string{phoneCall}},
		{"formatted with +91", "&q=%2B91%2098000-00001", []string{phoneCall}},
		{"national 0 prefix", "&q=09811111111", []string{otherCall}},
		{"matches the called number too", "&q=9822222222", []string{otherCall}},
		{"call id prefix", "&q=" + phoneCall[:8], []string{phoneCall}},
		{"search + status", "&q=98&status=ringing", []string{otherCall}},
		{"unknown status is empty, not an error", "&status=bogus", nil},
		{"no match", "&q=9999999999", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ids, total, code := list(c.query)
			if code != http.StatusOK {
				t.Fatalf("status %d", code)
			}
			if len(ids) != len(c.want) || total != len(c.want) {
				t.Fatalf("got ids=%v total=%d, want %v", ids, total, c.want)
			}
			for i := range ids {
				if ids[i] != c.want[i] {
					t.Fatalf("got %v, want %v", ids, c.want)
				}
			}
		})
	}
}

// TestListCallsSearchDigitID: a call id whose first block is all digits is
// found by that prefix, not mistaken for a phone number only.
func TestListCallsSearchDigitID(t *testing.T) {
	f := dbtest.Setup(t)
	const id = "15693408-4339-4bbc-917e-c554501827fd"
	if _, err := f.DB.Pool.Exec(context.Background(), `INSERT INTO calls (id, tenant_id, direction, status, from_number, to_number, leg_id)
		VALUES ($1, $2, 'inbound', 'ended', '+919800000001', '+919800000089', 'leg-d')`, id, f.TenantID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestServer(f)
	for _, q := range []string{id[:8], id[:13]} {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, authedRequest(t, s, f, "GET", "/v1/calls?page=1&q="+q, ""))
		var env struct {
			Items []struct{ ID string } `json:"items"`
			Total int                   `json:"total"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		if rec.Code != http.StatusOK || env.Total != 1 || len(env.Items) != 1 || env.Items[0].ID != id {
			t.Fatalf("q=%s: status %d, got %+v", q, rec.Code, env)
		}
	}
}
