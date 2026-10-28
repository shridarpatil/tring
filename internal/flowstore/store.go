// Package flowstore is a read-only loader for IVR flow definitions by id, used
// by the media gateway to run the IVR on the pion plane (internal/media/ivrdrive)
// without depending on the orchestrator. It reuses the shared SQL catalog and
// the transport-agnostic flow model (internal/ivr).
//
// The reads a call makes — DID → flow, the flow itself, a transfer's team
// settings, the tenant's hold music / transfer prompt — are cached in Redis
// (cache-aside, cacheTTL) when WithCache is set. The api invalidates each key
// on every write that could change it (the *CacheKey funcs); the TTL only
// bounds staleness if an invalidation is missed.
package flowstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/db"
	"github.com/shridarpatil/tring/internal/ivr"
	"github.com/shridarpatil/tring/internal/queries"
	"github.com/shridarpatil/tring/internal/routing"
)

// cacheTTL bounds how stale a cached DID or flow can be if an invalidation is
// ever missed (e.g. the api and gateway point at different Redis).
const cacheTTL = time.Hour

// FlowCacheKey is the cache key for a flow's name + definition. Exported so
// the api can invalidate it when the flow is edited or deleted.
func FlowCacheKey(flowID uuid.UUID) string { return "ivr:flow:" + flowID.String() }

// DIDCacheKey is the cache key for an inbound DID's tenant + flow. Exported so
// the api can invalidate it when the number, or the flow it points at, changes.
func DIDCacheKey(e164 string) string { return "ivr:did:" + e164 }

// TeamCacheKey is the cache key for a team's own queue settings. Exported so
// the api can invalidate it when the team is updated or deleted.
func TeamCacheKey(tenantID uuid.UUID, team string) string {
	return "team:config:" + tenantID.String() + ":" + team
}

// TenantAudioCacheKey is the cache key for a tenant's hold music + transfer
// failed prompt. Exported so the api can invalidate it on a settings change.
func TenantAudioCacheKey(tenantID uuid.UUID) string { return "tenant:audio:" + tenantID.String() }

type flowEntry struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	Name       string    `json:"name"`
	Definition string    `json:"definition"`
}

type didEntry struct {
	TenantID uuid.UUID `json:"tenant_id"`
	FlowID   string    `json:"flow_id"`
}

type tenantAudio struct {
	HoldMusic        string `json:"hold_music"`
	TransferFailFile string `json:"transfer_fail_file"`
}

// cached serves key from c when warm, else calls load and stores its result.
// A load error (including no rows) is returned and not cached.
func cached[T any](ctx context.Context, c *cache.Cache, key string, load func() (T, error)) (T, error) {
	var v T
	if b, ok := c.Get(ctx, key); ok && json.Unmarshal(b, &v) == nil {
		return v, nil
	}
	v, err := load()
	if err != nil {
		return v, err
	}
	if b, err := json.Marshal(v); err == nil {
		c.Set(ctx, key, b, cacheTTL)
	}
	return v, nil
}

// Store loads active flows by id. Read-only: it never mutates the DB.
type Store struct {
	db    *db.DB
	q     *queries.Queries
	cache *cache.Cache // nil = no caching (every read hits the DB)
}

// New builds a Store over an existing DB handle.
func New(d *db.DB) *Store {
	return &Store{db: d, q: queries.MustLoad()}
}

// WithCache caches DID and flow reads in c (nil-safe: a nil cache disables it).
func (s *Store) WithCache(c *cache.Cache) *Store {
	s.cache = c
	return s
}

// Load fetches the tenant's active flow with the given id and parses its
// definition. Returns an error for a bad id, an unknown/inactive flow, a flow
// owned by another tenant, or a malformed definition; (nil, nil) if the flow
// row exists but has an empty definition.
func (s *Store) Load(ctx context.Context, tenant, flowID string) (*ivr.Flow, error) {
	_, def, err := s.activeFlow(ctx, tenant, flowID)
	if err != nil {
		return nil, fmt.Errorf("load flow %s: %w", flowID, err)
	}
	return ivr.Parse([]byte(def))
}

// FlowName resolves just the tenant's active flow display name, reusing the
// same row Load reads. Used by the media plane to log the dialed flow as a web
// call's to_number without loading/parsing the whole graph.
func (s *Store) FlowName(ctx context.Context, tenant, flowID string) (string, error) {
	name, _, err := s.activeFlow(ctx, tenant, flowID)
	if err != nil {
		return "", fmt.Errorf("flow name %s: %w", flowID, err)
	}
	return name, nil
}

// activeFlow reads the name + definition of a tenant's active flow.
func (s *Store) activeFlow(ctx context.Context, tenant, flowID string) (name, def string, err error) {
	tid, err := uuid.Parse(tenant)
	if err != nil {
		return "", "", fmt.Errorf("bad tenant id %q: %w", tenant, err)
	}
	fid, err := uuid.Parse(flowID)
	if err != nil {
		return "", "", fmt.Errorf("bad flow id %q: %w", flowID, err)
	}
	key := FlowCacheKey(fid)
	if b, ok := s.cache.Get(ctx, key); ok {
		var e flowEntry
		// The key is the flow id alone, so check the tenant: a foreign flow id
		// must not resolve from the cache any more than from the DB.
		if json.Unmarshal(b, &e) == nil && e.TenantID == tid {
			return e.Name, e.Definition, nil
		}
	}
	var owner uuid.UUID
	if err := s.db.Pool.QueryRow(ctx, s.q.GetActiveFlowByID, fid, tid).Scan(&owner, &name, &def); err != nil {
		return "", "", err
	}
	if b, err := json.Marshal(flowEntry{TenantID: owner, Name: name, Definition: def}); err == nil {
		s.cache.Set(ctx, key, b, cacheTTL)
	}
	return name, def, nil
}

// ResolveDID returns the owning tenant + attached flow id for an inbound DID
// (empty flowID = the number exists but has no flow attached). Used by the media
// plane to route an inbound phone call (the PSTN plug) to its IVR. Read-only.
func (s *Store) ResolveDID(ctx context.Context, did string) (tenantID uuid.UUID, flowID string, err error) {
	// An unknown DID errors and isn't cached, so a number added later routes on
	// its first call.
	e, err := cached(ctx, s.cache, DIDCacheKey(did), func() (didEntry, error) {
		var e didEntry
		var fid *uuid.UUID
		if err := s.db.Pool.QueryRow(ctx, s.q.ResolveInboundNumber, did).Scan(&e.TenantID, &fid); err != nil {
			return e, err
		}
		if fid != nil {
			e.FlowID = fid.String()
		}
		return e, nil
	})
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("resolve DID %s: %w", did, err)
	}
	return e.TenantID, e.FlowID, nil
}

// TeamSIPs returns the SIP usernames of a team's members that are available for
// calls, in membership position order. Used by the media gateway's transfer ring
// to target the right agents. Delegates to routing.TeamMembers so the FreeSWITCH
// and pion planes share one definition of the lookup (no drift). Read-only.
func (s *Store) TeamSIPs(ctx context.Context, tenantID uuid.UUID, team string) ([]string, error) {
	return routing.TeamMembers(ctx, s.db, s.q, tenantID, team)
}

// TeamConfig loads a team's queue settings (strategy / ring_seconds / max_wait /
// resolved hold music) for the media gateway's transfer ring. The team's own
// row and the tenant's hold music are cached separately, so a change to either
// invalidates only its own key. Read-only.
func (s *Store) TeamConfig(ctx context.Context, tenantID uuid.UUID, team string) (routing.TeamConfig, bool, error) {
	cfg, err := cached(ctx, s.cache, TeamCacheKey(tenantID, team), func() (routing.TeamConfig, error) {
		return routing.LoadTeamConfig(ctx, s.db, s.q, tenantID, team)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return routing.TeamConfig{}, false, nil
	}
	if err != nil {
		return routing.TeamConfig{}, false, err
	}
	// Best-effort: unreadable tenant hold music falls back to the default, as
	// an unset one does.
	audio, _ := s.tenantAudio(ctx, tenantID)
	cfg.MOH = routing.EffectiveMOH(cfg.MOH, audio.HoldMusic)
	return cfg, true, nil
}

func (s *Store) tenantAudio(ctx context.Context, tenantID uuid.UUID) (tenantAudio, error) {
	return cached(ctx, s.cache, TenantAudioCacheKey(tenantID), func() (tenantAudio, error) {
		var a tenantAudio
		err := s.db.Pool.QueryRow(ctx, s.q.GetTenantAudio, tenantID).Scan(&a.HoldMusic, &a.TransferFailFile)
		return a, err
	})
}

// TenantHoldMusic returns the tenant's configured system hold music
// (tenants.hold_music), "" when unset. Used by the gateway's agent-initiated
// hold path to play music into the room. Read-only.
func (s *Store) TenantHoldMusic(ctx context.Context, tenantID uuid.UUID) (string, error) {
	a, err := s.tenantAudio(ctx, tenantID)
	return a.HoldMusic, err
}

// TenantTransferFailedAudio returns the synthesized "no one answered" prompt path
// played on a failed blind transfer, "" when unset (TTS was off at save time).
func (s *Store) TenantTransferFailedAudio(ctx context.Context, tenantID uuid.UUID) (string, error) {
	a, err := s.tenantAudio(ctx, tenantID)
	return a.TransferFailFile, err
}

// AgentAvailable reports the agent's backend-managed availability
// (available_for_calls — the integrator's Away/Break toggle). The gateway reads
// it when the agent's presence socket connects so the SDK can show Away.
func (s *Store) AgentAvailable(ctx context.Context, tenantID uuid.UUID, sip string) (bool, error) {
	var available bool
	if err := s.db.Pool.QueryRow(ctx, s.q.GetAgentAvailability, tenantID, sip).Scan(&available); err != nil {
		return false, err
	}
	return available, nil
}
