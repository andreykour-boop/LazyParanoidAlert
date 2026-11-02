//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"siren/internal/config"
	"siren/internal/logsetup"
)

// Service identity in the SCM; the name must stay stable once installed.
const (
	serviceName        = "siren"
	serviceDisplayName = "Siren security agent"
	serviceDescription = "Honeypot agent: decoy service listeners and RDP/SSH login sensors with Telegram alerts."
)

// stopTimeout bounds how long Execute waits for the pipeline after Stop.
const stopTimeout = 30 * time.Second

// isServiceMode reports whether the process was started by the Windows
// service control manager.
func isServiceMode() bool {
	is, err := svc.IsWindowsService()
	return err == nil && is
}

// sirenService adapts the agent pipeline to the SCM lifecycle.
type sirenService struct {
	cfgPath string
	done    chan struct{}
}

// Execute implements svc.Handler.
func (s *sirenService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (more bool, exitCode uint32) {
	changes <- svc.Status{State: svc.StartPending}

	// Services start in System32; resolve relative paths from the config
	// against the binary's directory instead.
	if exe, err := os.Executable(); err == nil {
		_ = os.Chdir(filepath.Dir(exe))
	}

	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		// Nothing else works without a config; stderr is invisible to
		// the SCM, so the exit code is the only channel left.
		fmt.Fprintln(os.Stderr, "siren:", err)
		return false, 1
	}
	log, cleanup, err := logsetup.New(cfg.LogLevel, cfg.LogFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "siren:", err)
		return false, 1
	}
	defer cleanup()

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := runAgent(runCtx, cfg, log); err != nil {
			log.Error("agent pipeline failed", "err", err)
		}
		close(s.done)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	log.Info("service running", "config", s.cfgPath)

	for req := range r {
		switch req.Cmd {
		case svc.Interrogate:
			changes <- req.CurrentStatus
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			cancel()
			select {
			case <-s.done:
			case <-time.After(stopTimeout):
				log.Warn("service stop timed out waiting for the pipeline")
			}
			return false, 0
		}
	}
	return false, 0
}

// runService runs the agent as a Windows service (used when SCM starts the
// binary).
func runService(cfgPath string) error {
	return svc.Run(serviceName, &sirenService{cfgPath: cfgPath, done: make(chan struct{})})
}

// serviceCommand implements the -service flag: install, remove, start,
// stop and status.
func serviceCommand(cmd, cfgPath, account, password string) error {
	switch cmd {
	case "install", "remove", "start", "stop", "status":
	default:
		return fmt.Errorf("unknown -service command %q (want install, remove, start, stop or status)", cmd)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (needs admin): %w", err)
	}
	defer m.Disconnect()

	switch cmd {
	case "install":
		return serviceInstall(m, cfgPath, account, password)
	case "remove":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return fmt.Errorf("service %q is not installed", serviceName)
		}
		defer s.Close()
		// Best-effort stop: removing a running service would fail.
		status, err := s.Query()
		if err == nil && status.State != svc.Stopped {
			if _, err := s.Control(svc.Stop); err == nil {
				waitStopped(s, 15*time.Second)
			}
		}
		if err := s.Delete(); err != nil {
			return fmt.Errorf("delete service: %w", err)
		}
		fmt.Printf("service %q removed\n", serviceName)
	case "start":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return fmt.Errorf("service %q is not installed", serviceName)
		}
		defer s.Close()
		if err := s.Start(); err != nil {
			return fmt.Errorf("start service: %w", err)
		}
		fmt.Printf("service %q started\n", serviceName)
	case "stop":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return fmt.Errorf("service %q is not installed", serviceName)
		}
		defer s.Close()
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop service: %w", err)
		}
		waitStopped(s, 15*time.Second)
		fmt.Printf("service %q stopped\n", serviceName)
	case "status":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return fmt.Errorf("service %q is not installed", serviceName)
		}
		defer s.Close()
		status, err := s.Query()
		if err != nil {
			return fmt.Errorf("query service: %w", err)
		}
		fmt.Printf("service %q state: %s\n", serviceName, serviceStateName(status.State))
	}
	return nil
}

// serviceInstall creates the service with automatic start. By default it
// runs as LocalSystem, which can read the Security log. With an explicit
// account, the installer tries to add it to the Event Log Readers group,
// which is the least privilege that satisfies the Security log sensor.
func serviceInstall(m *mgr.Mgr, cfgPath, account, password string) error {
	if _, err := m.OpenService(serviceName); err == nil {
		return fmt.Errorf("service %q is already installed (use -service remove first)", serviceName)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	absCfg, err := filepath.Abs(cfgPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	if _, err := os.Stat(absCfg); err != nil {
		return fmt.Errorf("config file %s: %w", absCfg, err)
	}

	conf := mgr.Config{
		StartType:    mgr.StartAutomatic,
		DisplayName:  serviceDisplayName,
		Description:  serviceDescription,
		Dependencies: []string{"EventLog"},
	}
	if account != "" {
		conf.ServiceStartName = account
		conf.Password = password
	}

	s, err := m.CreateService(serviceName, exe, conf, "-config", absCfg)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()
	fmt.Printf("service %q installed\n  binary: %s\n  config: %s\n", serviceName, exe, absCfg)

	if account != "" {
		// Least privilege: Security log reading needs Event Log Readers
		// membership; LocalSystem is already covered.
		if err := addToEventLogReaders(account); err != nil {
			fmt.Printf("  note: add %q to the \"Event Log Readers\" group manually (%v)\n", account, err)
		} else {
			fmt.Printf("  account %q added to \"Event Log Readers\"\n", account)
		}
	}
	return nil
}

// addToEventLogReaders adds account to the local Event Log Readers group.
func addToEventLogReaders(account string) error {
	out, err := exec.Command("net", "localgroup", "Event Log Readers", account, "/add").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(out))
	}
	return nil
}

// waitStopped polls until the service reports Stopped or the timeout ends.
func waitStopped(s *mgr.Service, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil || status.State == svc.Stopped {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// serviceStateName formats an SCM state for humans.
func serviceStateName(state svc.State) string {
	names := map[svc.State]string{
		svc.Stopped:         "stopped",
		svc.StartPending:    "start pending",
		svc.StopPending:     "stop pending",
		svc.Running:         "running",
		svc.ContinuePending: "continue pending",
		svc.PausePending:    "pause pending",
		svc.Paused:          "paused",
	}
	if name, ok := names[state]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", state)
}
