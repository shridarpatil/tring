package ivr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EvalTimingOutcome decides a Timing node's branch — "in_hours" or
// "out_of_hours" — for time t, reading the node's `schedule` config. The UI
// saves a per-weekday list: [{day, enabled, start_time "HH:MM", end_time "HH:MM"}].
// A day with enabled=false (e.g. Saturday/Sunday marked off) is always closed;
// an enabled day is open only between start_time and end_time. A node with no
// schedule defaults to open (back-compat). Shared by the simulator and the live
// executor so the test panel and real calls agree.
//
// t is evaluated in its own location (server-local for a live call). Per-tenant
// timezone is a follow-up; today the deployment TZ (e.g. Asia/Kolkata) governs.
func EvalTimingOutcome(config map[string]interface{}, t time.Time) string {
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

// ScheduleOpen reports whether a team's working-hours JSON (the Timing node's
// schedule shape) is open at t. Empty, null or [] means always open. Garbage
// also reads as open so a bad row never raises an alert; ValidateSchedule
// keeps garbage from being stored in the first place.
func ScheduleOpen(hours json.RawMessage, t time.Time) bool {
	if isEmptySchedule(hours) {
		return true
	}
	var v []interface{}
	if err := json.Unmarshal(hours, &v); err != nil {
		return true
	}
	return EvalTimingOutcome(map[string]interface{}{"schedule": v}, t) == "in_hours"
}

// ValidateSchedule checks a working-hours JSON before it is stored: an array
// of {day, enabled, start_time, end_time} with unique weekday names, and for
// enabled days HH:MM times where start is before end. Empty or [] is valid.
func ValidateSchedule(hours json.RawMessage) error {
	if isEmptySchedule(hours) {
		return nil
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(hours, &rows); err != nil {
		return errors.New("hours must be an array of day objects")
	}
	seen := map[string]bool{}
	for _, row := range rows {
		name, _ := row["day"].(string)
		day := strings.ToLower(name)
		if !validDays[day] {
			return fmt.Errorf("invalid day %q", name)
		}
		if seen[day] {
			return fmt.Errorf("duplicate day %q", day)
		}
		seen[day] = true
		enabled, ok := row["enabled"].(bool)
		if !ok {
			return fmt.Errorf("%s: enabled must be true or false", day)
		}
		if !enabled {
			continue
		}
		start, sok := hhmmToMinutes(row["start_time"])
		end, eok := hhmmToMinutes(row["end_time"])
		if !sok || !eok {
			return fmt.Errorf("%s: start_time and end_time must be HH:MM", day)
		}
		if start >= end {
			return fmt.Errorf("%s: start_time must be before end_time", day)
		}
	}
	return nil
}

var validDays = map[string]bool{
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true,
	"friday": true, "saturday": true, "sunday": true,
}

func isEmptySchedule(hours json.RawMessage) bool {
	h := bytes.TrimSpace(hours)
	return len(h) == 0 || bytes.Equal(h, []byte("null")) || bytes.Equal(h, []byte("[]"))
}
