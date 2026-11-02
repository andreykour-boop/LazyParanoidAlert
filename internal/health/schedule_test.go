package health

import (
	"testing"
	"time"
)

func at(y int, month time.Month, d, h, m int) time.Time {
	return time.Date(y, month, d, h, m, 0, 0, time.Local)
}

func TestNextDailyHour(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		hour int
		want time.Time
	}{
		{"before the hour", at(2026, 10, 30, 8, 0), 9, at(2026, 10, 30, 9, 0)},
		{"same hour earlier minute", at(2026, 10, 30, 9, 30), 9, at(2026, 10, 31, 9, 0)},
		{"exactly at the hour", at(2026, 10, 30, 9, 0), 9, at(2026, 10, 31, 9, 0)},
		{"after the hour", at(2026, 10, 30, 12, 0), 9, at(2026, 10, 31, 9, 0)},
		{"midnight check, late evening", at(2026, 10, 30, 23, 59), 0, at(2026, 10, 31, 0, 0)},
		{"midnight check, early morning", at(2026, 10, 30, 0, 1), 0, at(2026, 10, 31, 0, 0)},
		{"month boundary", at(2026, 10, 31, 12, 0), 9, at(2026, 11, 1, 9, 0)},
		{"year boundary", at(2026, 12, 31, 12, 0), 9, at(2027, 1, 1, 9, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextDailyHour(tc.now, tc.hour)
			if !got.Equal(tc.want) {
				t.Errorf("nextDailyHour(%v, %d) = %v, want %v", tc.now, tc.hour, got, tc.want)
			}
		})
	}
}

func TestNextWeeklyRun(t *testing.T) {
	// 2026-10-30 is a Friday.
	if wd := at(2026, 10, 30, 12, 0).Weekday(); wd != time.Friday {
		t.Fatalf("test assumption broken: 2026-10-30 is %v, want Friday", wd)
	}

	cases := []struct {
		name    string
		now     time.Time
		weekday int
		hour    int
		want    time.Time
	}{
		// Monday = 1; from Friday 12:00 the next Monday 09:00 is Nov 2.
		{"friday to monday", at(2026, 10, 30, 12, 0), 1, 9, at(2026, 11, 2, 9, 0)},
		// Friday report from Friday morning: today.
		{"same day before hour", at(2026, 10, 30, 8, 0), 5, 9, at(2026, 10, 30, 9, 0)},
		// Friday report from Friday after the hour: next Friday.
		{"same day after hour", at(2026, 10, 30, 9, 1), 5, 9, at(2026, 11, 6, 9, 0)},
		// Exactly at the hour counts as passed: next week.
		{"same day at hour", at(2026, 10, 30, 9, 0), 5, 9, at(2026, 11, 6, 9, 0)},
		// Sunday = 0, from Friday.
		{"friday to sunday", at(2026, 10, 30, 12, 0), 0, 9, at(2026, 11, 1, 9, 0)},
		// Saturday = 6, from Friday.
		{"friday to saturday", at(2026, 10, 30, 12, 0), 6, 9, at(2026, 10, 31, 9, 0)},
		// Week boundary across month.
		{"month boundary", at(2026, 10, 31, 12, 0), 1, 9, at(2026, 11, 2, 9, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextWeeklyRun(tc.now, tc.weekday, tc.hour)
			if !got.Equal(tc.want) {
				t.Errorf("nextWeeklyRun(%v, %d, %d) = %v, want %v", tc.now, tc.weekday, tc.hour, got, tc.want)
			}
		})
	}
}

// TestScheduleRecomputeAfterFire: recompute-after-firing keeps the calendar
// anchor. Simulated with a fake clock: after "firing" at the scheduled
// moment, the next computation from that instant lands exactly one day or
// one week later — the property that makes restarts and DST shifts safe.
func TestScheduleRecomputeAfterFire(t *testing.T) {
	now := at(2026, 10, 30, 9, 0) // Friday 09:00
	// Firing moment for the daily check is "now"; recompute → tomorrow.
	next := nextDailyHour(now, 9)
	if !next.Equal(at(2026, 10, 31, 9, 0)) {
		t.Fatalf("next daily = %v, want Oct 31 09:00", next)
	}
	after := nextDailyHour(next.Add(time.Minute), 9)
	if !after.Equal(at(2026, 11, 1, 9, 0)) {
		t.Fatalf("recompute after fire = %v, want Nov 1 09:00", after)
	}

	// Weekly: fired Monday 09:00, recompute → next Monday.
	monday := at(2026, 11, 2, 9, 0)
	nextW := nextWeeklyRun(monday.Add(time.Minute), 1, 9)
	if !nextW.Equal(at(2026, 11, 9, 9, 0)) {
		t.Fatalf("recompute after weekly fire = %v, want Nov 9 09:00", nextW)
	}
}

// TestScheduleDSTGapNeutrality: when a DST spring-forward removes the
// target wall time (no 02:00 on that day in some zones), the recomputing
// scheduler still converges: normalization moves the moment forward, the
// next cycle recomputes from actual time. With Go's time.Date the gap is
// normalized (02:00 -> 03:00), so the next firing is that same day.
func TestScheduleDSTGapNeutrality(t *testing.T) {
	// Choose a zone with a spring-forward gap: Europe/Berlin 2026-03-29
	// (last Sunday of March) 02:00 -> 03:00.
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	gapDay := time.Date(2026, 3, 29, 1, 30, 0, 0, loc) // 01:30, before the gap
	next := nextDailyHour(gapDay, 2)
	// time.Date normalizes the missing 02:00 to 03:00 same day.
	if next.Before(gapDay) || next.Day() != 29 {
		t.Errorf("DST gap handling: next = %v, want same-day normalized moment", next)
	}
	// And the recompute after firing lands on the next day 02:00.
	after := nextDailyHour(next.Add(time.Minute), 2)
	if after.Day() != 30 {
		t.Errorf("recompute after DST fire = %v, want Mar 30", after)
	}
}
