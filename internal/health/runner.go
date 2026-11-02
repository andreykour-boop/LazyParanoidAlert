package health

import (
	"context"
	"log/slog"
	"time"

	"siren/internal/event"
)

// Runner is the background disk-health watch. It runs one diff-check at
// startup (alarms on changes, never an "all is well" report), then follows
// the wall-clock schedule: a daily check at CheckHour local time and the
// weekly report on ReportWeekday at the same hour.
//
// Delivery is split per design: alarms about changes go through the event
// pipeline (KindDiskHealthAlert / KindDiskSpaceAlert) to get the unified
// alert format, while planned texts (weekly report, repeated daily space
// reminders) go through Report directly to the delivery queue, bypassing
// aggregation.
type Runner struct {
	// Collect produces one full snapshot; Collector.Snapshot in production.
	Collect func() Snapshot

	// DataDir holds the persisted snapshot between restarts.
	DataDir string

	// AgentLabel names the machine in planned reports ("NAME (ip)").
	AgentLabel string

	Thresholds    Thresholds
	CheckHour     int
	ReportWeekday int

	// Alarm emits one alarm through the event pipeline.
	Alarm func(kind event.Kind, detail string)
	// Report sends a planned message directly to delivery.
	Report func(text string)

	Now func() time.Time
	Log *slog.Logger
}

// Run performs the startup diff-check and serves the schedule until ctx is
// done. Collector/state errors are logged and turned into honest source
// errors, never into silence; only ctx cancel ends Run.
func (r *Runner) Run(ctx context.Context) error {
	prev, _, err := LoadState(r.DataDir)
	if err != nil {
		r.log().Warn("health: saved state unreadable, starting without diff base", "err", err)
	}
	cur := r.check(prev, false)

	for {
		now := r.now()
		nextDaily := nextDailyHour(now, r.CheckHour)
		nextWeekly := nextWeeklyRun(now, r.ReportWeekday, r.CheckHour)
		// The weekly slot also runs the daily checks; when both coincide
		// the weekly check wins (its report subsumes the daily diff).
		next := nextDaily
		weekly := false
		if !nextWeekly.After(nextDaily) {
			next = nextWeekly
			weekly = true
		}

		timer := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			cur = r.check(cur, weekly)
		}
	}
}

// check runs one full check: collect, diff against prev, dispatch alarms
// and space reminders (or the weekly report), persist the snapshot. The
// returned snapshot becomes the next diff base.
func (r *Runner) check(prev Snapshot, weekly bool) Snapshot {
	cur := r.Collect()

	if changes := Diff(prev, cur, r.Thresholds); len(changes) > 0 {
		r.log().Info("health: changes detected", "count", len(changes))
		r.Alarm(event.KindDiskHealthAlert, formatChanges(changes))
	}

	// Space violations: new ones alarm through the pipeline, continuing
	// ones produce the daily reminder delivered directly. On the first
	// run (no previous snapshot) every violation is new.
	prevLow := lowMounts(prev, r.Thresholds)
	var fresh, continuing []Volume
	for _, v := range SpaceViolations(cur, r.Thresholds) {
		if prev.CollectedAt.IsZero() || !prevLow[v.Mount] {
			fresh = append(fresh, v)
		} else {
			continuing = append(continuing, v)
		}
	}
	if len(fresh) > 0 {
		r.Alarm(event.KindDiskSpaceAlert, formatSpaceAlert(fresh, r.Thresholds))
	}
	if len(continuing) > 0 {
		r.Report(formatSpaceReminder(continuing, r.Thresholds))
	}

	if weekly {
		r.Report(WeeklyReport(cur, r.AgentLabel, r.Thresholds))
	}

	if err := SaveState(r.DataDir, cur); err != nil {
		r.log().Warn("health: cannot persist snapshot", "err", err)
	}
	return cur
}

// lowMounts lists the mounts below thresholds in a snapshot.
func lowMounts(snap Snapshot, th Thresholds) map[string]bool {
	low := make(map[string]bool)
	for _, v := range SpaceViolations(snap, th) {
		low[v.Mount] = true
	}
	return low
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}
