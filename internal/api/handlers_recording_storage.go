package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/shridarpatil/tring/internal/blobstore"
	"github.com/shridarpatil/tring/internal/safehttp"
)

// recordingStorage is the tenant's S3 recording storage as the console sees
// it. The secret is write-only: GET reports only whether one is set.
type recordingStorage struct {
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	Endpoint  string `json:"endpoint"`
	Prefix    string `json:"prefix"`
	UseSSL    bool   `json:"use_ssl"`
	AccessKey string `json:"access_key"`
	HasSecret bool   `json:"has_secret"`
	// Warning is set on a save that worked but needs attention (e.g. the test
	// file could not be deleted).
	Warning string `json:"warning,omitempty"`
}

type putRecordingStorage struct {
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	Endpoint  string `json:"endpoint"`
	Prefix    string `json:"prefix"`
	UseSSL    *bool  `json:"use_ssl"` // omitted = true
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"` // "" = keep the stored secret
}

var (
	s3BucketRe   = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]{1,61}[a-z0-9])?$`)
	s3RegionRe   = regexp.MustCompile(`^[a-z0-9-]{0,32}$`)
	s3EndpointRe = regexp.MustCompile(`^(([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?)?$`)
	s3PrefixRe   = regexp.MustCompile(`^[A-Za-z0-9._/-]{0,200}$`)
	s3AccessRe   = regexp.MustCompile(`^[\x21-\x7e]{0,128}$`)
)

// storageProbeTimeout bounds the test write on save.
const storageProbeTimeout = 15 * time.Second

// loadRecordingStorage reads the tenant's stored config; secret is as stored
// (sealed).
func (s *Server) loadRecordingStorage(ctx context.Context, tenant string) (blobstore.Config, error) {
	var c blobstore.Config
	err := s.db.Pool.QueryRow(ctx, s.q.GetTenantS3, tenant).Scan(
		&c.Bucket, &c.Region, &c.Endpoint, &c.Prefix, &c.UseSSL, &c.AccessKey, &c.SecretKey)
	return c, err
}

func storageView(c blobstore.Config) recordingStorage {
	return recordingStorage{
		Bucket: c.Bucket, Region: c.Region, Endpoint: c.Endpoint, Prefix: c.Prefix,
		UseSSL: c.UseSSL, AccessKey: c.AccessKey, HasSecret: c.SecretKey != "",
	}
}

func (s *Server) handleGetRecordingStorage(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	c, err := s.loadRecordingStorage(r.Context(), t.ID.String())
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "query failed", err)
		return
	}
	writeJSON(w, http.StatusOK, storageView(c))
}

// validateStorage checks the fields that reach the S3 client. The endpoint is
// tenant-supplied: an IP literal (or localhost) in a blocked range is refused
// here, and hostnames are checked again at dial time by the safehttp guard.
func (s *Server) validateStorage(req putRecordingStorage) string {
	switch {
	case !s3BucketRe.MatchString(req.Bucket):
		return "bucket must be 3-63 lowercase letters, digits, dots or hyphens"
	case !s3RegionRe.MatchString(req.Region):
		return "region may only contain lowercase letters, digits and hyphens"
	case !s3EndpointRe.MatchString(req.Endpoint):
		return "endpoint must be a host or IP, optionally with :port (no scheme or path)"
	case !s3PrefixRe.MatchString(req.Prefix) || strings.HasPrefix(req.Prefix, "/"):
		return "prefix may only contain letters, digits and . _ / - and must not start with /"
	case !s3AccessRe.MatchString(req.AccessKey):
		return "access_key must be printable ASCII without spaces"
	case req.AccessKey == "" && req.SecretKey != "":
		return "secret_key needs an access_key"
	case req.Bucket != "" && req.Endpoint == "" && req.Region == "":
		// Without it minio asks AWS for the bucket's region
		// (s3:GetBucketLocation), which a minimal recording policy lacks.
		return "region is required for AWS S3 (for example ap-south-1)"
	}
	if req.Endpoint != "" {
		host := req.Endpoint
		if h, _, ok := strings.Cut(strings.TrimPrefix(host, "["), "]"); ok && strings.HasPrefix(req.Endpoint, "[") {
			host = h
		} else if i := strings.LastIndex(host, ":"); i >= 0 {
			host = host[:i]
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			host = "127.0.0.1"
		}
		if a, err := netip.ParseAddr(host); err == nil && safehttp.Blocked(a, s.outboundAllow) {
			return "endpoint address not allowed (private or local; list it in TRING_OUTBOUND_ALLOW_CIDRS to permit)"
		}
	}
	return ""
}

// handlePutRecordingStorage saves the tenant's recording storage. A blank
// secret keeps the stored one; clearing the access key drops the secret too
// (back to the IAM role). With a bucket set, the config is test-written first
// and not saved if that fails.
func (s *Server) handlePutRecordingStorage(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	var req putRecordingStorage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Bucket, req.Region = strings.TrimSpace(req.Bucket), strings.TrimSpace(req.Region)
	req.Endpoint, req.Prefix = strings.TrimSpace(req.Endpoint), strings.Trim(strings.TrimSpace(req.Prefix), "/")
	req.AccessKey = strings.TrimSpace(req.AccessKey)
	if req.Prefix == "" {
		req.Prefix = "recordings"
	}
	useSSL := req.UseSSL == nil || *req.UseSSL
	if msg := s.validateStorage(req); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if req.SecretKey != "" && s.secrets == nil {
		writeError(w, http.StatusBadRequest, "secret_key needs TRING_SECRETS_KEY on the server (it is stored encrypted)")
		return
	}
	stored, err := s.loadRecordingStorage(r.Context(), t.ID.String())
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "query failed", err)
		return
	}

	// The secret the new config will use: the new one, the kept one, or none.
	var newSealed *string // nil = keep stored
	secret := ""
	switch {
	case req.AccessKey == "":
		empty := ""
		newSealed = &empty
	case req.SecretKey != "":
		sealed := s.sealSecret(req.SecretKey)
		newSealed, secret = &sealed, req.SecretKey
	case req.AccessKey != stored.AccessKey:
		// The stored secret belongs to the old key.
		writeError(w, http.StatusBadRequest, "enter the secret_key for the new access key")
		return
	case stored.SecretKey == "":
		writeError(w, http.StatusBadRequest, "secret_key is required with an access_key")
		return
	default:
		if secret, err = s.openSecret(stored.SecretKey); err != nil {
			s.writeServerError(w, r, http.StatusInternalServerError, "stored secret_key cannot be opened; enter it again", err)
			return
		}
	}

	warning := ""
	if req.Bucket != "" {
		cfg := blobstore.Config{Bucket: req.Bucket, Region: req.Region, Endpoint: req.Endpoint,
			Prefix: req.Prefix, UseSSL: useSSL, AccessKey: req.AccessKey, SecretKey: secret}
		// Always through the SSRF dial guard, even without the role's resolver.
		build := s.blob
		if build == nil {
			build = blobstore.NewResolver(nil, safehttp.Dial(s.outboundAllow))
		}
		var leftover string
		var deleteErr error
		store, err := build.Build(cfg)
		if err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), storageProbeTimeout)
			leftover, deleteErr, err = store.Probe(ctx, t.ID.String())
			cancel()
		}
		if err != nil {
			s.log.Warn("recording storage test write failed", "tenant_id", t.ID, "err", err)
			writeError(w, http.StatusBadRequest, "storage test failed: "+blobstore.Describe(err))
			return
		}
		if deleteErr != nil {
			s.log.Warn("recording storage test file not deleted", "tenant_id", t.ID, "key", leftover, "err", deleteErr)
			warning = "Saved. The test upload worked, but the test file " + leftover +
				" could not be deleted (" + blobstore.Describe(deleteErr) + "). Recordings will upload fine; " +
				"delete that file yourself, or grant s3:DeleteObject so future saves clean up."
		}
	}

	if _, err := s.db.Pool.Exec(r.Context(), s.q.UpdateTenantS3, t.ID,
		req.Bucket, req.Region, req.Endpoint, req.Prefix, useSSL, req.AccessKey, newSealed); err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "update failed", err)
		return
	}
	c, err := s.loadRecordingStorage(r.Context(), t.ID.String())
	if err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "query failed", err)
		return
	}
	view := storageView(c)
	view.Warning = warning
	writeJSON(w, http.StatusOK, view)
}

// handleClearRecordingStorageKeys drops the static keys; uploads then use the
// server's IAM role. Bucket and the rest are kept.
func (s *Server) handleClearRecordingStorageKeys(w http.ResponseWriter, r *http.Request) {
	t := tenantFromCtx(r.Context())
	if _, err := s.db.Pool.Exec(r.Context(), s.q.ClearTenantS3Keys, t.ID); err != nil {
		s.writeServerError(w, r, http.StatusInternalServerError, "update failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
