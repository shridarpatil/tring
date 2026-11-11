// Package campaign owns durable bulk outbound calling, independent of the media transport.
package campaign

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/tring/internal/ivr"
)

// Config is the editable delivery configuration. A launched campaign is immutable.
type Config struct {
	WebhookID        string `json:"webhook_id,omitempty"`
	Type             string `json:"type"`
	AudioID          string `json:"audio_id,omitempty"`
	Text             string `json:"text,omitempty"`
	FlowID           string `json:"flow_id,omitempty"`
	Team             string `json:"team,omitempty"`
	AgentWaitSeconds int    `json:"agent_wait_seconds,omitempty"`
	CallerID         string `json:"caller_id"`
	Timezone         string `json:"timezone"`
	WindowStart      string `json:"window_start"`
	WindowEnd        string `json:"window_end"`
	Concurrency      int    `json:"concurrency"`
	CallsPerMinute   int    `json:"calls_per_minute"`
	MaxAttempts      int    `json:"max_attempts"`
	RetryMinutes     int    `json:"retry_minutes"`
	RingSeconds      int    `json:"ring_seconds"`
	MaxSeconds       int    `json:"max_seconds"`
}

// Prepared contains immutable, tenant-owned playable assets.
type Prepared struct {
	Team      string    `json:"team,omitempty"`
	AudioFile string    `json:"audio_file,omitempty"`
	Flow      *ivr.Flow `json:"flow,omitempty"`
}

// Payload is frozen on each attempt, including test calls from editable drafts.
type Payload struct {
	Config   Config   `json:"config"`
	Prepared Prepared `json:"prepared"`
}

// Campaign is a campaign definition and its delivery progress.
type Campaign struct {
	NextStartAt time.Time      `json:"next_start_at"`
	Metrics     map[string]int `json:"metrics"`
	ID          uuid.UUID      `json:"id"`
	Name        string         `json:"name"`
	Status      string         `json:"status"`
	Config      Config         `json:"config"`
	Prepared    *Prepared      `json:"prepared"`
	Revision    int            `json:"revision"`
	ScheduledAt *time.Time     `json:"scheduled_at"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Counts      map[string]int `json:"counts"`
}

// Recipient is one normalized destination in a campaign.
type Recipient struct {
	ID            uuid.UUID `json:"id"`
	Phone         string    `json:"phone"`
	ExternalRef   string    `json:"external_ref"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
}

// Attempt tracks one dial, including test calls (no recipient ID).
type Attempt struct {
	ID          uuid.UUID  `json:"id"`
	CampaignID  uuid.UUID  `json:"campaign_id"`
	RecipientID *uuid.UUID `json:"recipient_id"`
	Phone       string     `json:"phone"`
	ExternalRef string     `json:"external_ref"`
	Attempt     int        `json:"attempt"`
	Status      string     `json:"status"`
	Payload     Payload    `json:"-"`
	CallID      *uuid.UUID `json:"call_id"`
	AnsweredAt  *time.Time `json:"answered_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	CreatedAt   time.Time  `json:"created_at"`
	Error       string     `json:"error"`
}

// Validate rejects settings that could create unbounded dialing or silent calls.
func (c Config) Validate() error {
	if c.WebhookID != "" {
		if _, err := uuid.Parse(c.WebhookID); err != nil {
			return errors.New("invalid webhook_id")
		}
	}
	switch c.Type {
	case "audio":
		if _, err := uuid.Parse(c.AudioID); err != nil {
			return errors.New("select an uploaded sound")
		}
	case "tts":
		if strings.TrimSpace(c.Text) == "" || len(c.Text) > 5000 || strings.Contains(c.Text, "{{") {
			return errors.New("TTS requires 1–5000 bytes of static text")
		}
	case "ivr":
		if _, err := uuid.Parse(c.FlowID); err != nil {
			return errors.New("select an IVR flow")
		}
	case "agent":
		if strings.TrimSpace(c.Team) == "" || len(c.Team) > 200 {
			return errors.New("select a team")
		}
		if c.AgentWaitSeconds < 5 || c.AgentWaitSeconds > 3600 {
			return errors.New("agent_wait_seconds must be 5–3600")
		}
	default:
		return errors.New("type must be audio, tts, ivr, or agent")
	}
	if c.CallerID == "" {
		return errors.New("caller_id is required")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil || c.Timezone == "" {
		return errors.New("invalid timezone")
	}
	start, err := time.Parse("15:04", c.WindowStart)
	if err != nil {
		return errors.New("window_start must be HH:MM")
	}
	end, err := time.Parse("15:04", c.WindowEnd)
	if err != nil || !end.After(start) {
		return errors.New("window_end must be after window_start on the same day")
	}
	if c.Concurrency < 1 || c.Concurrency > 100 || c.CallsPerMinute < 1 || c.CallsPerMinute > 300 {
		return errors.New("concurrency must be 1–100 and calls_per_minute 1–300")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 5 || c.RetryMinutes < 1 || c.RetryMinutes > 10080 {
		return errors.New("max_attempts must be 1–5 and retry_minutes 1–10080")
	}
	if c.RingSeconds < 5 || c.RingSeconds > 120 || c.MaxSeconds < 10 || c.MaxSeconds > 3600 {
		return errors.New("ring_seconds must be 5–120 and max_seconds 10–3600")
	}
	return nil
}

// InWindow evaluates a daily window in its IANA timezone, including DST transitions.
func (c Config) InWindow(now time.Time) bool {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return false
	}
	clock := now.In(loc).Format("15:04")
	return clock >= c.WindowStart && clock < c.WindowEnd
}

// JSON serializes internal, JSON-safe models.
func JSON(v any) []byte { b, _ := json.Marshal(v); return b }

// ErrConflict indicates a stale revision or an invalid lifecycle transition.
var ErrConflict = fmt.Errorf("campaign changed or action is not allowed in its current state")
