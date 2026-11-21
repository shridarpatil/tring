package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/secretbox"
)

const storagePath = "/v1/tenants/me/recording-storage"

func rawS3Secret(t *testing.T, f *dbtest.Fixture) (access, secret string) {
	t.Helper()
	if err := f.DB.Pool.QueryRow(context.Background(),
		`SELECT s3_access_key, s3_secret_key FROM tenants WHERE id = $1`, f.TenantID).Scan(&access, &secret); err != nil {
		t.Fatal(err)
	}
	return access, secret
}

// An empty bucket skips the test write, so these run without an S3 server.
func TestRecordingStorageRoundTrip(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSealingServer(f, testBox(t, 1))

	rec := serve(t, s, f, http.MethodPut, storagePath,
		`{"region":"ap-south-1","prefix":"rec","use_ssl":true,"access_key":"AKIAEXAMPLE","secret_key":"s3cr3t"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "s3cr3t") {
		t.Fatalf("secret echoed back: %s", rec.Body.String())
	}
	_, sealed := rawS3Secret(t, f)
	if !secretbox.IsSealed(sealed) {
		t.Fatalf("secret stored unsealed: %q", sealed)
	}

	// Saving again without secret_key keeps the stored one. Slashes around the
	// prefix are trimmed.
	rec = serve(t, s, f, http.MethodPut, storagePath,
		`{"region":"ap-south-1","prefix":"/rec2/","use_ssl":true,"access_key":"AKIAEXAMPLE"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("second put: %d %s", rec.Code, rec.Body.String())
	}
	if _, again := rawS3Secret(t, f); again != sealed {
		t.Fatal("blank secret_key replaced the stored secret")
	}

	rec = serve(t, s, f, http.MethodGet, storagePath, "")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	if got["prefix"] != "rec2" || got["access_key"] != "AKIAEXAMPLE" || got["has_secret"] != true {
		t.Errorf("get = %v", got)
	}
	if _, ok := got["secret_key"]; ok {
		t.Error("GET returned secret_key")
	}
}

func TestRecordingStorageRejects(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSealingServer(f, testBox(t, 1))
	for name, body := range map[string]string{
		"bad bucket":        `{"bucket":"Bad_Bucket"}`,
		"metadata ip":       `{"bucket":"rec","endpoint":"169.254.169.254"}`,
		"localhost":         `{"bucket":"rec","endpoint":"localhost:9000"}`,
		"loopback":          `{"bucket":"rec","endpoint":"127.0.0.1:9000"}`,
		"endpoint with ${":  `{"bucket":"rec","endpoint":"${x}"}`,
		"prefix with space": `{"prefix":"rec dir"}`,
		"secret no key":     `{"secret_key":"only-secret"}`,
	} {
		if rec := serve(t, s, f, http.MethodPut, storagePath, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
}

// Without TRING_SECRETS_KEY a secret would be stored in plain text: refused.
func TestRecordingStorageSecretNeedsKey(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSealingServer(f, nil)
	rec := serve(t, s, f, http.MethodPut, storagePath, `{"access_key":"AKIA","secret_key":"s"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "TRING_SECRETS_KEY") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecordingStorageClearKeys(t *testing.T) {
	f := dbtest.Setup(t)
	s := newSealingServer(f, testBox(t, 1))
	if _, err := f.DB.Pool.Exec(context.Background(),
		`UPDATE tenants SET s3_bucket='rec', s3_access_key='AKIA', s3_secret_key='x' WHERE id=$1`, f.TenantID); err != nil {
		t.Fatal(err)
	}
	if rec := serve(t, s, f, http.MethodDelete, storagePath+"/keys", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if a, sk := rawS3Secret(t, f); a != "" || sk != "" {
		t.Errorf("keys left: %q %q", a, sk)
	}
	var bucket string
	_ = f.DB.Pool.QueryRow(context.Background(), `SELECT s3_bucket FROM tenants WHERE id=$1`, f.TenantID).Scan(&bucket)
	if bucket != "rec" {
		t.Errorf("bucket = %q, want kept", bucket)
	}
}

func TestRecordingStorageNonAdmin(t *testing.T) {
	f := dbtest.Setup(t)
	if _, err := f.DB.Pool.Exec(context.Background(), "UPDATE users SET role='agent' WHERE id=$1", f.UserID); err != nil {
		t.Fatal(err)
	}
	s := newSealingServer(f, testBox(t, 1))
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if rec := serve(t, s, f, m, storagePath, `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s as agent: got %d, want 403", m, rec.Code)
		}
	}
}

// Saving with a bucket test-writes it: a store that refuses the write is
// reported and nothing is saved; one that accepts it is saved.
func TestRecordingStorageProbe(t *testing.T) {
	f := dbtest.Setup(t)
	for _, tc := range []struct {
		name   string
		status int
		want   int
	}{
		{"accepted", http.StatusOK, http.StatusOK},
		{"denied", http.StatusForbidden, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var methods []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method)
				if tc.status != http.StatusOK {
					w.Header().Set("Content-Type", "application/xml")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>no</Message></Error>`))
					return
				}
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("ETag", `"abc"`)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", GatewayPublicURL: "http://gw.test",
				Secrets: testBox(t, 1), OutboundAllow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}})
			body := `{"bucket":"rec-` + tc.name + `","region":"us-east-1","endpoint":"` +
				strings.TrimPrefix(srv.URL, "http://") + `","use_ssl":false,"access_key":"AKIA","secret_key":"s"}`
			rec := serve(t, s, f, http.MethodPut, storagePath, body)
			if rec.Code != tc.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			var bucket string
			_ = f.DB.Pool.QueryRow(context.Background(), `SELECT s3_bucket FROM tenants WHERE id=$1`, f.TenantID).Scan(&bucket)
			if saved := bucket == "rec-"+tc.name; saved != (tc.want == http.StatusOK) {
				t.Errorf("bucket saved = %v (%q)", saved, bucket)
			}
			if tc.want == http.StatusOK && strings.Join(methods, ",") != "PUT,DELETE" {
				t.Errorf("store saw %v, want PUT then DELETE", methods)
			}
			if tc.want != http.StatusOK && !strings.Contains(rec.Body.String(), "AccessDenied") {
				t.Errorf("error should carry the S3 code: %s", rec.Body.String())
			}
		})
	}
}
