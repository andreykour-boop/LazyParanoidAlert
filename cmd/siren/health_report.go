package main

import (
	"fmt"
	"time"

	"siren/internal/config"
	"siren/internal/health"
)

// printHealthReport serves -health-report: it collects a fresh disk health
// snapshot and prints it in the human-readable report format — sizes
// recalculated to GB, thresholds marked, RAID-less vendor-CLI noise
// filtered. This is the headless-machine counterpart of the control UI's
// "Check now": strictly read-only, nothing is saved to the state file and
// nothing is sent to Telegram.
func printHealthReport(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	l1, l2, vols := health.NewPlatformSources()
	c := &health.Collector{L1Sources: l1, L2Sources: l2, VolumesSources: vols}
	snap := c.Snapshot()

	th := health.Thresholds{}
	if cfg.Health != nil {
		th = health.Thresholds{WarnPct: cfg.Health.WarnPct, MinFreeGB: cfg.Health.MinFreeGB}
	}

	fmt.Printf("Проверка дисков — %s, %s (живой сбор; состояние не сохранялось)\n\n",
		cfg.AgentName, time.Now().Format("2006-01-02 15:04"))
	fmt.Println(health.SnapshotText(snap, th))
	return nil
}
