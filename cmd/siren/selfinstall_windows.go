//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"siren/internal/config"
)

// First-run self-installation: the interactive launch places the agent
// into %ProgramFiles%\siren, bootstraps a config from defaults, installs
// the service (firewall rule included) and, when the config already
// carries real credentials, starts it. Running an exe from anywhere else
// with the service installed is an update: the installed copy is replaced
// and the service returns to its previous state. Headless modes
// (-console, -service, -health-report) never trigger this.

// Placeholder values of the bootstrap config; the service must not start
// while they are in place (the same convention install.ps1 used).
const (
	placeholderToken  = "REPLACE-WITH-BOT-TOKEN"
	placeholderChatID = "REPLACE-WITH-CHAT-ID"
)

// installDir is the agent home: the service, the config and the state
// live here, regardless of where the exe was downloaded to.
func installDir() string {
	root := os.Getenv("ProgramFiles")
	if root == "" {
		root = `C:\Program Files`
	}
	return filepath.Join(root, "siren")
}

func installExePath() string { return filepath.Join(installDir(), "siren.exe") }

func installCfgPath() string { return filepath.Join(installDir(), "config.json") }

// firstRunActions is the decision part of the first run: what setup must
// do for the current machine state. Pure and cheap to test; the executor
// below just applies it step by step.
type firstRunActions struct {
	copySelf        bool // the running exe is not the installed copy
	stopService     bool // the service holds the file we must replace
	bootstrapConfig bool // no config at the install path yet
	installService  bool // the service is not installed
}

// planFirstRun decides the actions from the current exe path, the SCM
// state of the service ("не установлена"/"остановлена"/"работает"…) and
// the config path. sameInstallPath reports whether exePath already is
// the installed copy.
func planFirstRun(exePath, svcState, cfgPath string) firstRunActions {
	var a firstRunActions
	a.copySelf = !strings.EqualFold(filepath.Clean(exePath), installExePath())
	// The running service holds the installed file open: stop it before
	// the replacement copy is written.
	a.stopService = a.copySelf && svcState == "работает"
	if _, err := os.Stat(cfgPath); err != nil {
		a.bootstrapConfig = true
	}
	a.installService = svcState == "не установлена"
	return a
}

// configIsTemplate reports whether the config still carries placeholder
// credentials, i.e. the service must stay stopped until the owner fills
// token/chat_id in the control panel.
func configIsTemplate(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	return cfg.Telegram.Token == "" || cfg.Telegram.ChatID == "" ||
		strings.Contains(cfg.Telegram.Token, "REPLACE-WITH") ||
		strings.Contains(cfg.Telegram.ChatID, "REPLACE-WITH")
}

// defaultAgentConfig builds the bootstrap config: desktop profile traps,
// loopback allowlist (own trap tests must not alert) and placeholder
// credentials. Built in code instead of an embedded file, so it can never
// drift from config.Save validation.
func defaultAgentConfig(agentName string) *config.Config {
	return &config.Config{
		AgentName:              agentName,
		Profile:                config.ProfileDesktop,
		Telegram:               config.Telegram{Token: placeholderToken, ChatID: placeholderChatID},
		AggregateWindowSeconds: 300,
		Allowlist:              []string{"127.0.0.1", "::1"},
		DataDir:                "data",
		LogLevel:               "info",
		LogFile:                "siren.log",
		Health:                 config.DefaultHealth(),
	}
}

// bootstrapConfig creates the config at path; an existing file is never
// touched.
func bootstrapConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "windows-pc"
	}
	if err := config.Save(path, defaultAgentConfig(name)); err != nil {
		return fmt.Errorf("create config %s: %w", path, err)
	}
	return nil
}

// runFirstRunSetup applies the plan: stop the service if it holds the
// target, copy the exe there and relaunch from the install path (the
// caller exits), or bootstrap the config and ensure the service. The
// returned error is advisory for the panel: the UI opens regardless, and
// the manual "Установить и запустить" button drives the same commands.
func runFirstRunSetup(cfgPath string) (relaunched bool, err error) {
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("resolve executable: %w", err)
	}
	state, err := serviceState()
	if err != nil {
		return false, fmt.Errorf("query service: %w", err)
	}
	plan := planFirstRun(exe, state, installCfgPath())

	if plan.stopService {
		_ = serviceCommand("stop", cfgPath, "", "")
	}
	if plan.copySelf {
		if err := copyFile(exe, installExePath()); err != nil {
			return false, fmt.Errorf("self-copy to %s: %w", installExePath(), err)
		}
		if relaunched, err = relaunchSelf(installCfgPath()); relaunched || err != nil {
			return relaunched, err
		}
	}
	if plan.bootstrapConfig {
		if err := bootstrapConfig(installCfgPath()); err != nil {
			return false, err
		}
	}
	if plan.installService {
		if err := serviceCommand("install", installCfgPath(), "", ""); err != nil {
			return false, err
		}
		state, err = serviceState()
		if err != nil {
			return false, err
		}
	}
	// A configured config brings the service up after an update or a
	// moved install; a placeholder keeps it stopped for the panel.
	if cfg, err := config.Load(installCfgPath()); err == nil && !configIsTemplate(cfg) &&
		(state == "остановлена" || state == "не установлена") {
		if err := serviceCommand("start", installCfgPath(), "", ""); err != nil {
			return false, err
		}
	}
	return false, nil
}

// copyFile overwrites dst with src (binary replace for updates).
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

// relaunchSelf starts the installed exe with the same arguments, forcing
// the config to the install path so the child does not depend on the
// caller's working directory.
func relaunchSelf(cfgPath string) (bool, error) {
	args := os.Args[1:]
	if !hasConfigFlag(args) {
		own := make([]string, 0, len(args)+2)
		own = append(own, args...)
		args = append(own, "-config", cfgPath)
	}
	cmd := exec.Command(installExePath(), args...)
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("relaunch from %s: %w", installExePath(), err)
	}
	return true, nil
}

// hasConfigFlag reports whether the arguments already carry -config.
func hasConfigFlag(args []string) bool {
	for _, a := range args {
		if a == "-config" || a == "--config" {
			return true
		}
		if strings.HasPrefix(a, "-config=") || strings.HasPrefix(a, "--config=") {
			return true
		}
	}
	return false
}
