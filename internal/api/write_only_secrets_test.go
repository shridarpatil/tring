package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

func insertRegisterTrunk(t *testing.T, f *dbtest.Fixture, password string) string {
	t.Helper()
	var id string
	if err := f.DB.Pool.QueryRow(context.Background(), `
		INSERT INTO trunks (tenant_id, name, sip_username, sip_password, register)
		VALUES ($1, 'carrier', 'user1', $2, true) RETURNING id
	`, f.TenantID, password).Scan(&id); err != nil {
		t.Fatalf("insert trunk: %v", err)
	}
	return id
}

// sip_password is write-only: GET returns has_password, never the secret.
func TestGetTrunkHidesPassword(t *testing.T) {
	f := dbtest.Setup(t)
	id := insertRegisterTrunk(t, f, "s3cret-carrier-pw")
	s := newTestServer(f)

	rec := serve(t, s, f, http.MethodGet, "/v1/trunks/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "s3cret-carrier-pw") || strings.Contains(body, "sip_password") {
		t.Errorf("GET /v1/trunks/{id} exposes the password: %s", body)
	}
	var got struct {
		HasPassword bool `json:"has_password"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.HasPassword {
		t.Errorf("has_password = %v (err %v), want true", got.HasPassword, err)
	}
}

// The console edits a trunk without re-entering the password, so an empty
// sip_password on update keeps the stored one; a non-empty one replaces it.
func TestUpdateTrunkKeepsPasswordOnEmpty(t *testing.T) {
	f := dbtest.Setup(t)
	id := insertRegisterTrunk(t, f, "original-pw")
	s := newTestServer(f)
	ctx := context.Background()

	update := func(pw *string) string {
		t.Helper()
		row := f.DB.Pool.QueryRow(ctx, s.q.UpdateTrunk,
			nil, nil, nil, pw, nil, nil, nil, nil, nil, nil, id, f.TenantID)
		tr, err := scanTrunk(row)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		return tr.SIPPassword
	}
	empty, next := "", "rotated-pw"
	if got := update(nil); got != "original-pw" {
		t.Errorf("omitted password: got %q, want original-pw", got)
	}
	if got := update(&empty); got != "original-pw" {
		t.Errorf("empty password: got %q, want original-pw", got)
	}
	if got := update(&next); got != "rotated-pw" {
		t.Errorf("new password: got %q, want rotated-pw", got)
	}
}

// Agents never SIP-register on the gateway transport, so the generated SIP
// password is stored (FreeSWITCH directory, legacy transport) but not returned.
func TestCreateAgentOmitsSipPassword(t *testing.T) {
	f := dbtest.Setup(t)
	s := newTestServer(f)

	rec := serve(t, s, f, http.MethodPost, "/v1/agents", `{"sip_username":"alice"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sip_password") {
		t.Errorf("create agent returns sip_password: %s", rec.Body.String())
	}
	var stored string
	if err := f.DB.Pool.QueryRow(context.Background(),
		`SELECT sip_password FROM agents WHERE tenant_id = $1 AND sip_username = 'alice'`, f.TenantID,
	).Scan(&stored); err != nil || stored == "" {
		t.Errorf("stored sip_password = %q (err %v), want a generated password", stored, err)
	}
}
