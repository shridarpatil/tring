package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shridarpatil/tring/internal/dbtest"
)

// An .ogg upload was trusted to be Ogg/Opus and stored untouched. Ogg/Vorbis
// then reached callers through the Opus prompt player as garbage. It must be
// transcoded to Opus like any other non-Opus upload.
func TestUploadOggVorbisIsTranscodedToOpus(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg required")
	}
	src := filepath.Join(t.TempDir(), "v.ogg")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-f", "lavfi", "-i", "sine=f=440:d=0.3",
		"-c:a", "libvorbis", "-y", src).CombinedOutput(); err != nil {
		t.Skipf("no libvorbis: %v %s", err, out)
	}
	opus := filepath.Join(t.TempDir(), "o.ogg")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-f", "lavfi", "-i", "sine=f=440:d=0.3",
		"-c:a", "libopus", "-y", opus).CombinedOutput(); err != nil {
		t.Skipf("no libopus: %v %s", err, out)
	}
	if !isOggOpus(opus) || isOggOpus(src) {
		t.Fatalf("isOggOpus: opus=%v vorbis=%v, want true/false", isOggOpus(opus), isOggOpus(src))
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	f := dbtest.Setup(t)
	dir := t.TempDir()
	s := NewServer(Config{DB: f.DB, JWTSecret: "test-secret", GatewayPublicURL: "http://gw.test", AudioDir: dir})

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "vorbis.ogg")
	_, _ = fw.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/audio", &body)
	req.Header.Set("Authorization", authedRequest(t, s, f, http.MethodGet, "/", "").Header.Get("Authorization"))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var a Audio
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(s.diskPathFor(a.FilePath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stored[:min(len(stored), 128)], []byte("OpusHead")) {
		t.Fatalf("stored %s is not Ogg/Opus (first page: %q)", a.FilePath, stored[:min(len(stored), 64)])
	}
}
