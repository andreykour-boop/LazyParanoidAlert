// Command siren is a cross-platform honeypot agent: it opens decoy listeners
// on configurable ports, watches SSH/RDP authentication events on occupied
// ports, enriches each fact with source host data and delivers aggregated
// alerts to a Telegram chat.
//
// Launch modes: the service control manager starts the headless pipeline;
// -service install|start|stop|remove|status manages the SCM entry; an
// interactive launch (double-click, terminal) runs the pipeline plus the
// Windows control UI (tray icon + settings window); -console forces the
// plain console pipeline everywhere.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"siren/internal/config"
	"siren/internal/logsetup"
)

// version is the agent build version, set by build scripts via -ldflags
// "-X main.version=...". Do not assign it elsewhere.
var version = "0.1.0-dev"

// defaultConfigPath is the built-in -config default.
const defaultConfigPath = "config.json"

// resolveConfigPath makes the default config discoverable when siren is
// launched from another directory (e.g. `sudo /opt/siren/siren
// -health-report` from anywhere): if the default relative config.json is
// missing in the working directory, fall back to the one next to the
// executable — the same rule the Windows control UI applies to data_dir.
func resolveConfigPath(path string) string {
	if path != defaultConfigPath {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), defaultConfigPath)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return path
}

func main() {
	configPath := flag.String("config", defaultConfigPath, "path to the agent JSON config")
	showVersion := flag.Bool("version", false, "print version and exit")
	consoleMode := flag.Bool("console", false, "run the pipeline in console mode, without the control UI")
	healthReport := flag.Bool("health-report", false, "collect disk health once, print a human-readable report and exit (read-only)")
	serviceCmd := flag.String("service", "", "Windows service management: install, remove, start, stop, status")
	serviceAccount := flag.String("service-account", "", "run the service under this account (install only)")
	servicePassword := flag.String("service-password", "", "password for -service-account")
	flag.Parse()

	// The default config is resolved against the executable directory too,
	// so headless one-shot modes work from any working directory.
	cfgPath := resolveConfigPath(*configPath)

	initConsole()

	if *showVersion {
		fmt.Println("siren", version)
		return
	}

	// One-shot human-readable disk report for headless machines.
	if *healthReport {
		if err := printHealthReport(cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "siren:", err)
			os.Exit(1)
		}
		return
	}

	// Service management subcommands need admin rights, not service mode.
	if *serviceCmd != "" {
		if err := serviceCommand(*serviceCmd, cfgPath, *serviceAccount, *servicePassword); err != nil {
			fmt.Fprintln(os.Stderr, "siren:", err)
			os.Exit(1)
		}
		return
	}

	// Started by the service control manager: the SCM owns the lifecycle.
	if isServiceMode() {
		if err := runService(cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "siren:", err)
			os.Exit(1)
		}
		return
	}

	if *consoleMode {
		if err := runConsole(cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "siren:", err)
			os.Exit(1)
		}
		return
	}

	if err := runInteractive(cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "siren:", err)
		os.Exit(1)
	}
}

// runConsole is the classic foreground pipeline: load config, set up the
// logger, run until interrupted.
func runConsole(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	log, cleanup, err := logsetup.New(cfg.LogLevel, cfg.LogFile)
	if err != nil {
		return err
	}
	defer cleanup()

	log.Info("config loaded",
		"agent", cfg.AgentName,
		"traps", len(cfg.Traps),
		"version", version,
	)

	if err := runAgent(context.Background(), cfg, log); err != nil {
		log.Error("agent stopped with error", "err", err)
		return err
	}
	log.Info("agent stopped cleanly")
	return nil
}
