package health

import (
	"strings"
	"testing"
)

// TestSpaceAlertFormat: the alarm/reminder body names the volume, total
// size, free space (GB and %) and the configured threshold.
func TestSpaceAlertFormat(t *testing.T) {
	viol := []Volume{
		{Mount: "D:", TotalBytes: gb(3600), FreeBytes: gb(288)},
		{Mount: "G:", TotalBytes: gb(64), FreeBytes: gb(2), ReadOnly: true},
	}
	got := formatSpaceAlert(viol, Thresholds{WarnPct: 10, MinFreeGB: 20})

	for _, want := range []string{
		"D: — всего 3600.0 ГБ, свободно 288.0 ГБ (8.0%)",
		"G: (съёмный)",
		"порог: 10% или 20 ГБ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("space alert missing %q:\n%s", want, got)
		}
	}

	// Percent-only threshold when no absolute floor is configured.
	got = formatSpaceAlert(viol[:1], Thresholds{WarnPct: 10})
	if !strings.Contains(got, "порог: 10%") || strings.Contains(got, "или") {
		t.Errorf("threshold line wrong:\n%s", got)
	}
}

// TestSpaceAlertRepeatPerCheck: two consecutive daily checks with the
// volume still low produce two reminder texts — once per check, never more
// (the runner runs one check per day by schedule).
func TestSpaceAlertRepeatPerCheck(t *testing.T) {
	dir := t.TempDir()

	first := fullSnapshot(1)
	f := newRunnerFixture(t, dir, first)
	f.runner.check(Snapshot{}, false) // first run: alarm

	second := fullSnapshot(2)
	f2 := newRunnerFixture(t, dir, second)
	f2.runner.check(first, false) // still low: reminder

	third := fullSnapshot(3)
	f3 := newRunnerFixture(t, dir, third)
	f3.runner.check(second, false) // still low: reminder again

	// Each check produced exactly one planned text about space.
	if len(f2.reports) != 1 || len(f3.reports) != 1 {
		t.Fatalf("reminders per check = %d, %d; want 1, 1", len(f2.reports), len(f3.reports))
	}
	if !strings.Contains(f2.reports[0], "/data") || !strings.Contains(f3.reports[0], "/data") {
		t.Errorf("reminders must name the violating volume: %q / %q", f2.reports[0], f3.reports[0])
	}
}
