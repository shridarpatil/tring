package ivr

import (
	"encoding/json"
	"testing"
	"time"
)

func TestScheduleOpen(t *testing.T) {
	mon := func(h, m int) time.Time { return time.Date(2026, 11, 2, h, m, 0, 0, time.Local) } // Monday
	tue10 := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	nine5 := json.RawMessage(`[{"day":"monday","enabled":true,"start_time":"09:00","end_time":"17:00"}]`)
	off := json.RawMessage(`[{"day":"monday","enabled":false}]`)

	cases := []struct {
		name  string
		hours json.RawMessage
		at    time.Time
		want  bool
	}{
		{"empty", json.RawMessage(``), mon(10, 0), true},
		{"null", json.RawMessage(`null`), mon(10, 0), true},
		{"empty array", json.RawMessage(`[]`), mon(10, 0), true},
		{"inside", nine5, mon(10, 0), true},
		{"after close", nine5, mon(18, 0), false},
		{"opening minute", nine5, mon(9, 0), true},
		{"closing minute", nine5, mon(17, 0), false},
		{"day missing", nine5, tue10, false},
		{"day disabled", off, mon(10, 0), false},
		{"garbage", json.RawMessage(`not json`), mon(10, 0), true},
	}
	for _, c := range cases {
		if got := ScheduleOpen(c.hours, c.at); got != c.want {
			t.Errorf("%s: ScheduleOpen = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestValidateSchedule(t *testing.T) {
	ok := []string{
		``,
		`[]`,
		`null`,
		`[{"day":"monday","enabled":true,"start_time":"09:00","end_time":"17:00"}]`,
		`[{"day":"Sunday","enabled":false}]`,
	}
	for _, s := range ok {
		if err := ValidateSchedule(json.RawMessage(s)); err != nil {
			t.Errorf("ValidateSchedule(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{
		`{}`,
		`not json`,
		`[1]`,
		`[{"day":"funday","enabled":false}]`,
		`[{"day":"monday","enabled":false},{"day":"Monday","enabled":false}]`,
		`[{"day":"monday","enabled":true,"start_time":"25:00","end_time":"26:00"}]`,
		`[{"day":"monday","enabled":true,"start_time":"09:00","end_time":"24:00"}]`,
		`[{"day":"monday","enabled":true,"start_time":"17:00","end_time":"09:00"}]`,
		`[{"day":"monday","enabled":true,"start_time":"09:00","end_time":"09:00"}]`,
		`[{"day":"monday","enabled":true}]`,
		`[{"day":"monday","enabled":"yes"}]`,
	}
	for _, s := range bad {
		if err := ValidateSchedule(json.RawMessage(s)); err == nil {
			t.Errorf("ValidateSchedule(%q) = nil, want error", s)
		}
	}
}
