package routing

import (
	"context"
	"strconv"

	"github.com/shridarpatil/tring/internal/cache"
	"github.com/shridarpatil/tring/internal/queuekeys"
)

// PublishDepth writes a source's queue depth for a team to Redis so the API can
// sum phone + web waiters for the dashboard. Each producer publishes its own
// source key (queuekeys.SourcePhone / SourceWeb) so they don't clobber; the read
// side sums them. No-op without a cache.
func PublishDepth(ctx context.Context, c *cache.Cache, tenantID, team, source string, n int) {
	if c == nil {
		return
	}
	c.Set(ctx, queuekeys.DepthKey(tenantID, team, source),
		[]byte(strconv.Itoa(n)), queuekeys.DepthTTL)
}
