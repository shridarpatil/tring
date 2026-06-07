package orchestrator

import (
	"fmt"
	"strings"
	"time"
)

// evalTimingOutcome decides a Timing node's branch — "in_hours" or
// "out_of_hours" — for time t, reading the node's `schedule` config. The UI
// saves a per-weekday list: [{day, enabled, start_time "HH:MM", end_time "HH:MM"}].
// A day with enabled=false (e.g. Saturday/Sunday marked off) is always closed;
// an enabled day is open only between start_time and end_time. A node with no
// schedule defaults to open (back-compat). Shared by the simulator and the live
// executor so the test panel and real calls agree.
//
// t is evaluated in its own location (server-local for a live call). Per-tenant
// timezone is a follow-up; today the deployment TZ (e.g. Asia/Kolkata) governs.
func evalTimingOutcome(config map[string]interface{}, t time.Time) string {
	sched, ok := config["schedule"].([]interface{})
	if !ok || len(sched) == 0 {
		return "in_hours" // no schedule configured → open
	}
	want := strings.ToLower(t.Weekday().String()) // "saturday", …
	for _, raw := range sched {
		day, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := day["day"].(string)
		if !strings.EqualFold(name, want) {
			continue
		}
		// Today's row.
		if enabled, _ := day["enabled"].(bool); !enabled {
			return "out_of_hours" // day marked off (holiday/weekend)
		}
		start, sok := hhmmToMinutes(day["start_time"])
		end, eok := hhmmToMinutes(day["end_time"])
		if !sok || !eok {
			return "in_hours" // enabled but no/invalid hours → open all day
		}
		mins := t.Hour()*60 + t.Minute()
		if mins >= start && mins < end {
			return "in_hours"
		}
		return "out_of_hours"
	}
	return "out_of_hours" // today isn't in the schedule → closed
}

// hhmmToMinutes parses "HH:MM" into minutes since midnight.
func hhmmToMinutes(v interface{}) (int, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
