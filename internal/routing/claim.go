package routing

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

const claimTTL = 30 * time.Second

// Claim holds an agent slot until Release, renewing it even after ringing ends.
// Lost closes if renewal fails; callers must stop the ring/call in that case.
type Claim struct {
	*claimLease
	once sync.Once
}

type claimLease struct {
	store      Reservations
	key, owner string
	done, lost chan struct{}
	mu         sync.Mutex
	refs       int
}

func Acquire(ctx context.Context, store Reservations, tenant, sip string) *Claim {
	return acquire(ctx, store, ResKey(tenant, sip), claimTTL)
}

func acquire(ctx context.Context, store Reservations, key string, ttl time.Duration) *Claim {
	c := &Claim{claimLease: &claimLease{store: store, key: key, owner: uuid.NewString(), done: make(chan struct{}), lost: make(chan struct{}), refs: 1}}
	if !store.Reserve(ctx, key, c.owner, ttl) {
		return nil
	}
	go func() {
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), ttl/6)
				ok := store.Renew(ctx, key, c.owner, ttl)
				cancel()
				if !ok {
					select {
					case <-c.done:
						return
					default:
					}
					close(c.lost)
					return
				}
			}
		}
	}()
	return c
}

func (c *Claim) Done() <-chan struct{} { return c.done }
func (c *Claim) Lost() <-chan struct{} { return c.lost }

// Retain gives an answering participant its own release handle before the
// router withdraws the invitation. The lease survives until both handles end.
func (c *Claim) Retain() *Claim {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.lost:
		return nil
	default:
	}
	if c.refs == 0 {
		return nil
	}
	c.refs++
	return &Claim{claimLease: c.claimLease}
}

func (c *Claim) Release() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		c.mu.Lock()
		c.refs--
		last := c.refs == 0
		if last {
			close(c.done)
		}
		c.mu.Unlock()
		if last {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c.store.Release(ctx, c.key, c.owner)
		}
	})
}

// ClaimBinder lets the transport attach claims to invitations. An answer must
// Retain its claim before the invitation is consumed or withdrawn.
type ClaimBinder interface {
	BindClaims(map[string]*Claim)
}
