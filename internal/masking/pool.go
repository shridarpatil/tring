package masking

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrNoAvailableDID  = errors.New("no available masked numbers in pool")
	ErrSessionNotFound = errors.New("masked session not found")
)

// Session represents an active masked call pairing
type Session struct {
	MaskedDID string
	PartyA    string // e.g. customer extension or number
	PartyB    string // e.g. agent extension or number
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Pool manages masked number allocation and session lookups
type Pool struct {
	mu      sync.RWMutex
	allDIDs []string            // all available masked DIDs
	active  map[string]*Session // maskedDID → session
}

// NewPool creates a masking pool with the given DIDs
func NewPool(dids []string) *Pool {
	return &Pool{
		allDIDs: dids,
		active:  make(map[string]*Session),
	}
}

// Assign allocates a masked number for a conversation between two parties
func (p *Pool) Assign(partyA, partyB string) (*Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Clean up expired sessions first
	p.cleanExpired()

	// Check if there's already an active session for these parties
	for _, s := range p.active {
		if (s.PartyA == partyA && s.PartyB == partyB) ||
			(s.PartyA == partyB && s.PartyB == partyA) {
			return s, nil
		}
	}

	// Find an available DID
	for _, did := range p.allDIDs {
		if _, used := p.active[did]; !used {
			session := &Session{
				MaskedDID: did,
				PartyA:    partyA,
				PartyB:    partyB,
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(2 * time.Hour),
			}
			p.active[did] = session
			return session, nil
		}
	}

	return nil, ErrNoAvailableDID
}

// Lookup finds a session by masked DID
func (p *Pool) Lookup(maskedDID string) (*Session, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	session, ok := p.active[maskedDID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	if time.Now().After(session.ExpiresAt) {
		return nil, ErrSessionNotFound
	}

	return session, nil
}

// Resolve determines who to bridge to based on who is calling
func (p *Pool) Resolve(maskedDID, callerID string) (bridgeTo string, err error) {
	session, err := p.Lookup(maskedDID)
	if err != nil {
		return "", err
	}

	switch callerID {
	case session.PartyA:
		return session.PartyB, nil
	case session.PartyB:
		return session.PartyA, nil
	default:
		return "", errors.New("caller not part of this masked session")
	}
}

// Release frees a masked number back to the pool
func (p *Pool) Release(maskedDID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.active, maskedDID)
}

// ActiveSessions returns count of active sessions
func (p *Pool) ActiveSessions() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.active)
}

func (p *Pool) cleanExpired() {
	now := time.Now()
	for did, session := range p.active {
		if now.After(session.ExpiresAt) {
			delete(p.active, did)
		}
	}
}
