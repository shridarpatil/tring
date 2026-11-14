package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/blobstore"
	"github.com/shridarpatil/tring/internal/dbtest"
)

// fakeS3 serves GET /<bucket>/<key> for one object, writing the body in
// chunks with a pause between them so a download outlives a short deadline.
// status != 200 answers with an S3-style XML error instead.
func fakeS3(t *testing.T, body string, chunks int, pause time.Duration, status int, code string) *blobstore.Store {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>`+code+`</Code><Message>x</Message></Error>`)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		step := len(body) / chunks
		for i := 0; i < chunks; i++ {
			end := (i + 1) * step
			if i == chunks-1 {
				end = len(body)
			}
			_, _ = io.WriteString(w, body[i*step:end])
			w.(http.Flusher).Flush()
			if i < chunks-1 {
				time.Sleep(pause)
			}
		}
	}))
	t.Cleanup(srv.Close)
	st, err := blobstore.New(blobstore.Config{
		Bucket: "rec", Region: "us-east-1", Endpoint: strings.TrimPrefix(srv.URL, "http://"),
		AccessKey: "k", SecretKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func seedS3Call(t *testing.T, f *dbtest.Fixture) string {
	t.Helper()
	var id string
	if err := f.DB.Pool.QueryRow(context.Background(), `INSERT INTO calls (tenant_id, direction, status, from_number, to_number, leg_id, recording_s3_key)
		VALUES ($1, 'inbound', 'ended', '+919800000001', '+919800000089', 'leg-s3', 'recordings/t/leg-s3.mp3') RETURNING id`, f.TenantID).Scan(&id); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

// The recording routes sit behind middleware.Timeout(30s). A download still
// streaming when that deadline fires used to be cut mid-body after the 200
// had gone out, so the client saved a truncated recording as a success. The
// request deadline here stands in for that timeout.
func TestRecordingDownloadOutlivesRequestDeadline(t *testing.T) {
	f := dbtest.Setup(t)
	id := seedS3Call(t, f)
	body := strings.Repeat("0123456789", 3000)
	s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", GatewayPublicURL: "http://gw.test",
		Blob: fakeS3(t, body, 3, 300*time.Millisecond, http.StatusOK, "")})

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 200*time.Millisecond)
		defer cancel()
		s.Router().ServeHTTP(w, r.WithContext(ctx))
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req := authedRequest(t, s, f, "GET", srv.URL+"/v1/calls/"+id+"/recording", "")
	req.RequestURI = ""
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || err != nil || len(got) != len(body) {
		t.Fatalf("status %d, read err %v, got %d of %d bytes", resp.StatusCode, err, len(got), len(body))
	}
}

// A store failure before any byte is sent must be an error status, and only a
// missing object is a 404.
func TestRecordingDownloadStoreErrors(t *testing.T) {
	f := dbtest.Setup(t)
	id := seedS3Call(t, f)
	for _, tc := range []struct {
		status int
		code   string
		want   int
	}{
		{http.StatusNotFound, "NoSuchKey", http.StatusNotFound},
		{http.StatusForbidden, "AccessDenied", http.StatusBadGateway},
	} {
		s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", GatewayPublicURL: "http://gw.test",
			Blob: fakeS3(t, "", 1, 0, tc.status, tc.code)})
		rec := serve(t, s, f, "GET", "/v1/calls/"+id+"/recording", "")
		if rec.Code != tc.want {
			t.Errorf("S3 %s: got %d, want %d", tc.code, rec.Code, tc.want)
		}
	}
}
