package signaling

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/shridarpatil/tring/internal/media/sfu"
)

func TestValidDTMF(t *testing.T) {
	for _, d := range []string{"0", "5", "9", "*", "#", "A", "D"} {
		if !validDTMF(d) {
			t.Errorf("validDTMF(%q) = false, want true", d)
		}
	}
	for _, d := range []string{"", "12", "E", "a", " ", "1&x=y", "{", "\x00", "١"} {
		if validDTMF(d) {
			t.Errorf("validDTMF(%q) = true, want false", d)
		}
	}
}

// clientFrame encodes one masked client→server frame.
func clientFrame(t *testing.T, op ws.OpCode, fin bool, payload []byte) []byte {
	t.Helper()
	f := ws.NewFrame(op, fin, payload)
	f = ws.MaskFrameInPlace(f)
	var buf bytes.Buffer
	if err := ws.WriteFrame(&buf, f); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	return buf.Bytes()
}

// rw adapts a byte stream into the io.ReadWriter readClientText expects.
type rw struct {
	*bytes.Reader
	out bytes.Buffer
}

func (r *rw) Write(p []byte) (int, error) { return r.out.Write(p) }

func TestReadClientTextCapsSize(t *testing.T) {
	ok := clientFrame(t, ws.OpText, true, []byte(`{"type":"ping"}`))
	got, err := readClientText(&rw{Reader: bytes.NewReader(ok)}, 64)
	if err != nil || string(got) != `{"type":"ping"}` {
		t.Fatalf("small frame: %q, %v", got, err)
	}

	big := clientFrame(t, ws.OpText, true, bytes.Repeat([]byte("a"), 65))
	if _, err := readClientText(&rw{Reader: bytes.NewReader(big)}, 64); err == nil {
		t.Fatal("oversize single frame accepted")
	}

	// Fragments each under the frame cap must not add up past the message cap.
	var frag []byte
	frag = append(frag, clientFrame(t, ws.OpText, false, bytes.Repeat([]byte("a"), 40))...)
	frag = append(frag, clientFrame(t, ws.OpContinuation, true, bytes.Repeat([]byte("a"), 40))...)
	if _, err := readClientText(&rw{Reader: bytes.NewReader(frag)}, 64); !errors.Is(err, errMessageTooLarge) {
		t.Fatalf("oversize fragmented message: err = %v, want errMessageTooLarge", err)
	}
}

func limitsServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	sf, err := sfu.New(sfu.Config{})
	if err != nil {
		t.Fatalf("new sfu: %v", err)
	}
	secret := []byte("limits-secret")
	ts := httptest.NewServer(New(Config{SFU: sf, JWTSecret: secret}).Handler())
	t.Cleanup(ts.Close)
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant": uuid.New().String(), "sip_user": "a",
		"iss": "tring", "aud": "tring.sdk",
		"exp": time.Now().Add(time.Minute).Unix(),
	}).SignedString(secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return ts, strings.Replace(ts.URL, "http://", "ws://", 1) + "/rtc?token=" + tok
}

// waitClosed reports whether the server closes the socket within d.
func waitClosed(c net.Conn, d time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(d))
	for {
		if _, err := wsutil.ReadServerText(c); err != nil {
			var ne net.Error
			return !(errors.As(err, &ne) && ne.Timeout())
		}
	}
}

// An authenticated socket that never sends its join/register is dropped.
func TestIdleBeforeJoinTimesOut(t *testing.T) {
	old := joinTimeout
	joinTimeout = 200 * time.Millisecond
	t.Cleanup(func() { joinTimeout = old })

	_, u := limitsServer(t)
	c, _, _, err := ws.Dial(context.Background(), u)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if !waitClosed(c, 3*time.Second) {
		t.Fatal("idle socket still open after the join timeout")
	}
}

// One oversized message gets the connection closed, not buffered.
func TestOversizeMessageClosesConn(t *testing.T) {
	_, u := limitsServer(t)
	c, _, _, err := ws.Dial(context.Background(), u)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	big := `{"type":"join","room":"` + strings.Repeat("r", maxWSMessageBytes) + `"}`
	_ = wsutil.WriteClientMessage(c, ws.OpText, []byte(big))
	if !waitClosed(c, 3*time.Second) {
		t.Fatal("socket still open after an oversize message")
	}
}
