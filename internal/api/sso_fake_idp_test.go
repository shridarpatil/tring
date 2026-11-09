package api

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// fakeIdP is a minimal OIDC provider for tests: discovery document, JWKS and
// a /token endpoint that accepts any code and returns an ID token signed with
// key. The claims come from the knobs below, which a test sets (setClaims)
// before driving the callback.
type fakeIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey
	// issuer is srv.URL; it is the OIDC issuer the discovery doc claims.
	issuer string

	mu            sync.Mutex
	clientID      string // aud
	nonce         string
	email         string
	emailVerified any // bool or string, as IdPs differ
	gotVerifier   string
}

// setClaims sets what the next ID token says.
func (idp *fakeIdP) setClaims(nonce, email string, emailVerified any) {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	idp.nonce, idp.email, idp.emailVerified = nonce, email, emailVerified
}

// verifier is the PKCE code_verifier the last /token request carried.
func (idp *fakeIdP) verifier() string {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	return idp.gotVerifier
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key, clientID: "cid", emailVerified: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.issuer,
			"jwks_uri":                              idp.issuer + "/jwks",
			"authorization_endpoint":                idp.issuer + "/authorize",
			"token_endpoint":                        idp.issuer + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "test-key", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		idp.mu.Lock()
		idp.gotVerifier = r.PostForm.Get("code_verifier")
		now := time.Now()
		claims := map[string]any{
			"iss": idp.issuer, "aud": idp.clientID, "sub": "fake-subject",
			"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
			"nonce": idp.nonce, "email": idp.email, "email_verified": idp.emailVerified,
		}
		idp.mu.Unlock()
		payload, _ := json.Marshal(claims)
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256,
			Key: jose.JSONWebKey{Key: key, KeyID: "test-key"}}, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jws, err := signer.Sign(payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		raw, _ := jws.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access", "token_type": "Bearer", "expires_in": 300, "id_token": raw,
		})
	})
	idp.srv = httptest.NewServer(mux)
	idp.issuer = idp.srv.URL
	t.Cleanup(idp.srv.Close)
	return idp
}
