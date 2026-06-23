package api

import (
	"testing"
	"time"
)

func TestHourlyBuckets(t *testing.T) {
	now := time.Date(2026, 6, 23, 14, 37, 0, 0, time.UTC)

	t.Run("always 12 buckets ending at now's hour, oldest first", func(t *testing.T) {
		b := hourlyBuckets(nil, now)
		if len(b) != statsHourlyWindow {
			t.Fatalf("want %d buckets, got %d", statsHourlyWindow, len(b))
		}
		last := b[len(b)-1].Hour
		if !last.Equal(time.Date(2026, 6, 23, 14, 0, 0, 0, time.UTC)) {
			t.Fatalf("last bucket should be 14:00, got %s", last)
		}
		first := b[0].Hour
		if !first.Equal(time.Date(2026, 6, 23, 3, 0, 0, 0, time.UTC)) {
			t.Fatalf("first bucket should be 03:00, got %s", first)
		}
		for i := 1; i < len(b); i++ {
			if !b[i].Hour.Equal(b[i-1].Hour.Add(time.Hour)) {
				t.Fatalf("buckets not contiguous at %d", i)
			}
		}
	})

	t.Run("nil counts => all zero", func(t *testing.T) {
		for _, bk := range hourlyBuckets(nil, now) {
			if bk.Count != 0 {
				t.Fatalf("want 0, got %d at %s", bk.Count, bk.Hour)
			}
		}
	})

	t.Run("known hours fill, gaps stay zero", func(t *testing.T) {
		h14 := time.Date(2026, 6, 23, 14, 0, 0, 0, time.UTC)
		h13 := h14.Add(-time.Hour)
		hOld := time.Date(2026, 6, 23, 1, 0, 0, 0, time.UTC) // outside the window → ignored
		counts := map[int64]int{h14.Unix(): 21, h13.Unix(): 5, hOld.Unix(): 99}

		b := hourlyBuckets(counts, now)
		if got := b[len(b)-1].Count; got != 21 {
			t.Fatalf("current hour want 21, got %d", got)
		}
		if got := b[len(b)-2].Count; got != 5 {
			t.Fatalf("prev hour want 5, got %d", got)
		}
		// every other bucket is a gap → 0; the out-of-window 99 must not appear
		for i := 0; i < len(b)-2; i++ {
			if b[i].Count != 0 {
				t.Fatalf("want gap 0 at %d, got %d", i, b[i].Count)
			}
		}
	})
}
