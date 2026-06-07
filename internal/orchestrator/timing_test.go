package orchestrator

import (
	"testing"
	"time"
)

func schedule(days ...map[string]interface{}) map[string]interface{} {
	s := make([]interface{}, len(days))
	for i, d := range days {
		s[i] = d
	}
	return map[string]interface{}{"schedule": s}
}

func day(name string, enabled bool, start, end string) map[string]interface{} {
	return map[string]interface{}{"day": name, "enabled": enabled, "start_time": start, "end_time": end}
}

func TestEvalTimingOutcome(t *testing.T) {
	cfg := schedule(
		day("monday", true, "09:00", "18:00"),
		day("saturday", false, "09:00", "18:00"), // marked off (holiday)
		day("sunday", false, "00:00", "00:00"),   // marked off
	)

	// Saturday is disabled → closed regardless of the hour (the reported bug).
	sat := time.Date(2026, 6, 6, 14, 0, 0, 0, time.UTC) // a Saturday, 14:00
	if got := evalTimingOutcome(cfg, sat); got != "out_of_hours" {
		t.Errorf("Saturday 14:00 (disabled) = %q, want out_of_hours", got)
	}

	// Monday within hours → open.
	monIn := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC) // Monday 10:00
	if got := evalTimingOutcome(cfg, monIn); got != "in_hours" {
		t.Errorf("Monday 10:00 = %q, want in_hours", got)
	}
	// Monday outside hours → closed.
	monOut := time.Date(2026, 6, 8, 20, 0, 0, 0, time.UTC) // Monday 20:00
	if got := evalTimingOutcome(cfg, monOut); got != "out_of_hours" {
		t.Errorf("Monday 20:00 = %q, want out_of_hours", got)
	}

	// A weekday not listed in the schedule → closed.
	tue := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC) // Tuesday (absent)
	if got := evalTimingOutcome(cfg, tue); got != "out_of_hours" {
		t.Errorf("unlisted Tuesday = %q, want out_of_hours", got)
	}

	// No schedule at all → open (back-compat).
	if got := evalTimingOutcome(map[string]interface{}{}, sat); got != "in_hours" {
		t.Errorf("no schedule = %q, want in_hours", got)
	}
}
