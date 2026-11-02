package health

import "time"

// nextDailyHour returns the next occurrence of hour:00 local time strictly
// after now. It is a pure function of now, so the scheduler can recompute
// it every cycle: DST shifts simply move the next firing, and a process
// restart never shifts the calendar anchor.
func nextDailyHour(now time.Time, hour int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// nextWeeklyRun returns the next occurrence of weekday at hour:00 local
// time strictly after now. Weekday follows time.Weekday numbering
// (0 = Sunday, 1 = Monday, ...).
func nextWeeklyRun(now time.Time, weekday, hour int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	for {
		if t.After(now) && int(t.Weekday()) == weekday {
			return t
		}
		t = t.AddDate(0, 0, 1)
	}
}
