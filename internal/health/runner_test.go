package health

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"siren/internal/event"
)

// runnerFixture builds a Runner over fake sources with recorded alarms and
// reports.
type runnerFixture struct {
	alarms  []string // "kind: first-line-of-detail"
	reports []string
	runner  *Runner
}

func newRunnerFixture(t *testing.T, dataDir string, snap Snapshot) *runnerFixture {
	t.Helper()
	f := &runnerFixture{}
	f.runner = &Runner{
		Collect:       func() Snapshot { return snap },
		DataDir:       dataDir,
		AgentLabel:    "TEST-01 (10.0.0.1)",
		Thresholds:    Thresholds{WarnPct: 10},
		CheckHour:     9,
		ReportWeekday: 1,
		Alarm: func(kind event.Kind, detail string) {
			first, _, _ := strings.Cut(detail, "\n")
			f.alarms = append(f.alarms, string(kind)+": "+first)
		},
		Report: func(text string) { f.reports = append(f.reports, text) },
		Log:    tLogger(t),
	}
	return f
}

// tLogger silences the runner logs in tests.
func tLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunnerStartupHealthySilent(t *testing.T) {
	dir := t.TempDir()
	f := newRunnerFixture(t, dir, baseSnapshot(1))

	f.runner.check(Snapshot{}, false)

	if len(f.alarms) != 0 {
		t.Errorf("healthy startup must be silent, got alarms %v", f.alarms)
	}
	if len(f.reports) != 0 {
		t.Errorf("no planned texts on a plain check, got %v", f.reports)
	}
	// State persisted for the next diff.
	if _, ok, err := LoadState(dir); err != nil || !ok {
		t.Errorf("snapshot not persisted: ok=%v err=%v", ok, err)
	}
}

func TestRunnerStartupDiffAlarms(t *testing.T) {
	dir := t.TempDir()
	// Previous state: healthy disk.
	prev := baseSnapshot(1)
	if err := SaveState(dir, prev); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	// Now the disk degraded (change happened while the agent was down).
	f := newRunnerFixture(t, dir, func() Snapshot {
		cur := baseSnapshot(2)
		cur.L1[1].Status = StatusWarn
		cur.L1[1].Realloc = 8
		return cur
	}())

	f.runner.check(prev, false)

	if len(f.alarms) != 1 {
		t.Fatalf("alarms = %v, want exactly one", f.alarms)
	}
	if !contains(f.alarms[0], "disk_health_alert") || !contains(f.alarms[0], "sdb") {
		t.Errorf("alarm = %q, want disk_health_alert about sdb", f.alarms[0])
	}
}

// TestRunnerReallocGrowthAlarm: realloc 481 -> 482 between checks produces
// exactly one disk_health_alert carrying the old -> new values; a stabilized
// counter on the next check is silent.
func TestRunnerReallocGrowthAlarm(t *testing.T) {
	dir := t.TempDir()
	prev := baseSnapshot(1)
	prev.L1[0].Realloc = 481
	if err := SaveState(dir, prev); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	cur := baseSnapshot(2)
	cur.L1[0].Realloc = 482
	f := newRunnerFixture(t, dir, cur)
	f.runner.check(prev, false)

	if len(f.alarms) != 1 {
		t.Fatalf("alarms = %v, want exactly one realloc alarm", f.alarms)
	}
	if !contains(f.alarms[0], "disk_health_alert") || !contains(f.alarms[0], "realloc 481 -> 482") {
		t.Errorf("alarm = %q, want disk_health_alert with realloc 481 -> 482", f.alarms[0])
	}

	// Counter stabilized: the next check must be silent.
	stable := baseSnapshot(3)
	stable.L1[0].Realloc = 482
	f.alarms = nil
	f2 := newRunnerFixture(t, dir, stable)
	f2.runner.check(cur, false)
	if len(f2.alarms) != 0 {
		t.Errorf("stabilized realloc must be silent, alarms = %v", f2.alarms)
	}
}

func TestRunnerSpaceAlarmNewVsReminder(t *testing.T) {
	dir := t.TempDir()

	// First run, no prev: violation is new -> alarm through the pipeline.
	f := newRunnerFixture(t, dir, fullSnapshot(1))
	f.runner.check(Snapshot{}, false)
	foundAlarm := false
	for _, a := range f.alarms {
		if contains(a, "disk_space_alert") {
			foundAlarm = true
		}
	}
	if !foundAlarm {
		t.Errorf("new violation must alarm, alarms = %v", f.alarms)
	}
	if len(f.reports) != 0 {
		t.Errorf("first run must not send reminders, got %v", f.reports)
	}

	// Second check, still low: no new alarm, daily reminder via Report.
	f.alarms = nil
	stillLow := fullSnapshot(2)
	f2 := newRunnerFixture(t, dir, stillLow)
	prev := fullSnapshot(1)
	f2.runner.check(prev, false)
	for _, a := range f2.alarms {
		if contains(a, "disk_space_alert") {
			t.Errorf("continuing violation must not re-alarm: %v", f2.alarms)
		}
	}
	if len(f2.reports) != 1 || !contains(f2.reports[0], "свободно") {
		t.Errorf("want exactly one reminder with sizes, reports = %v", f2.reports)
	}
}

func TestRunnerWeeklyReport(t *testing.T) {
	dir := t.TempDir()
	prev := baseSnapshot(1)
	f := newRunnerFixture(t, dir, baseSnapshot(2))

	f.runner.check(prev, true)

	if len(f.reports) != 1 {
		t.Fatalf("reports = %d, want 1 weekly report", len(f.reports))
	}
	rep := f.reports[0]
	for _, want := range []string{
		"Еженедельный отчёт о дисках — TEST-01 (10.0.0.1)",
		"Тома:", "Массивы/тома:", "Диски:",
	} {
		if !contains(rep, want) {
			t.Errorf("report missing %q:\n%s", want, rep)
		}
	}
	// Minimalism: nothing failed on the fixture machine, so the "could not
	// check" section prints nothing at all — no empty-marker line.
	if contains(rep, "Не смог проверить") {
		t.Errorf("empty errors section must not print:\n%s", rep)
	}
	// Identical weekly reports must both be delivered: the Report path is
	// direct, so a second identical run still produces the text.
	f.runner.check(baseSnapshot(3), true)
	if len(f.reports) != 2 || f.reports[0] == "" {
		t.Fatalf("two identical weekly reports must both be produced, got %d", len(f.reports))
	}
}

func TestRunnerWeeklyReportListsErrors(t *testing.T) {
	dir := t.TempDir()
	snap := baseSnapshot(2)
	snap.Errors = []SourceError{{Source: "smartctl", Reason: "not found in PATH"}}
	f := newRunnerFixture(t, dir, snap)

	f.runner.check(Snapshot{}, true)

	if len(f.reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(f.reports))
	}
	rep := f.reports[0]
	if !contains(rep, "Не смог проверить:\n  smartctl: not found in PATH") {
		t.Errorf("lost source must be named in the report:\n%s", rep)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
