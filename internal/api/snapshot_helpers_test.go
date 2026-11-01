package api

import (
	"context"

	"github.com/google/uuid"
)

// Compile-time assertion that the snapshot helpers exist with the expected
// signatures (used by the SSE snapshot builder in Task 5).
var (
	_ func(*Server, context.Context, uuid.UUID) ([]Call, error)                = (*Server).activeCallsList
	_ func(*Server, context.Context, *Tenant) ([]Agent, error)                 = (*Server).agentsList
	_ func(*Server, context.Context, uuid.UUID) (statsOverview, error)         = (*Server).statsOverview
	_ func(*Server, context.Context, *Tenant, []Agent) (queuesResponse, error) = (*Server).queuesSnapshot
)
