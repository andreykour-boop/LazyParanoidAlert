package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// Emulator names accepted in Trap.Emulator. The set must stay in sync with
// internal/trap, which maps these names to protocol implementations.
const (
	EmulatorSilent   = "silent"
	EmulatorHTTP     = "http"
	EmulatorFTP      = "ftp"
	EmulatorTelnet   = "telnet"
	EmulatorVNC      = "vnc"
	EmulatorMySQL    = "mysql"
	EmulatorPostgres = "postgres"
	EmulatorSSH      = "ssh"
	EmulatorRDP      = "rdp"
)

// Profile names select default trap sets. Ports of a trusted LAN's background
// services (SMB/RPC 445, 135, 139; domain services 53, 88, 389, 464, 3268)
// are deliberately absent from every profile: they carry legitimate traffic.
const (
	ProfileDesktop = "desktop"
	ProfileServer  = "server"
)

// Trap enables one decoy listener. Emulator is one of the Emulator* names;
// TLS wraps the emulator into a TLS server (intended for port 443).
type Trap struct {
	Port     int    `json:"port"`
	Enabled  bool   `json:"enabled"`
	Emulator string `json:"emulator"`
	TLS      bool   `json:"tls,omitempty"`
}

// Telegram holds Bot API delivery settings.
type Telegram struct {
	Token  string `json:"token"`
	ChatID string `json:"chat_id"`

	// APIURL overrides the public Bot API endpoint (https://api.telegram.org);
	// intended for tests and air-gapped integrations. Optional.
	APIURL string `json:"api_url,omitempty"`
}

// Health configures the disk health watch (internal/health). A nil section
// (absent from the JSON file) means the watch runs with DefaultHealth, so
// pre-watch configs keep loading unchanged.
type Health struct {
	// Enabled turns the watch on; set false to silence it entirely.
	Enabled bool `json:"enabled"`

	// CheckHour is the local hour of the daily health check, 0..23.
	CheckHour int `json:"check_hour"`

	// ReportWeekday is the local weekday of the weekly report in
	// time.Weekday numbering (0 = Sunday, 1 = Monday, ... 6 = Saturday).
	ReportWeekday int `json:"report_weekday"`

	// WarnPct is the free-space alert threshold in percent of volume size.
	WarnPct int `json:"warn_pct"`

	// MinFreeGB is the absolute free-space floor in GiB; 0 disables it.
	MinFreeGB int `json:"min_free_gb"`
}

// DefaultHealth returns the watch settings used when the config file has no
// health section: on, daily check at 09:00, weekly report on Monday,
// free-space threshold 10%, no absolute floor.
func DefaultHealth() *Health {
	return &Health{Enabled: true, CheckHour: 9, ReportWeekday: 1, WarnPct: 10}
}

// Config is the full agent configuration as read from a JSON file.
type Config struct {
	// AgentName identifies this machine in alerts, e.g. "WIN-PC01".
	AgentName string `json:"agent_name"`

	// Profile optionally fills Traps with defaults for the machine role;
	// "" means no defaults and requires an explicit Traps list.
	Profile string `json:"profile,omitempty"`

	// Traps is the effective trap list: profile defaults merged with the
	// explicit entries (explicit entries win by port).
	Traps []Trap `json:"traps,omitempty"`

	Telegram Telegram `json:"telegram"`

	// AggregateWindowSeconds is the per-source aggregation window for
	// repeated events of the same type.
	AggregateWindowSeconds int `json:"aggregate_window_seconds"`

	// Allowlist holds source IPs or CIDR nets that never trigger alerts
	// (legitimate scanners such as antivirus or monitoring).
	Allowlist []string `json:"allowlist,omitempty"`

	// NameMap maps source IP to a human-readable machine name; it takes
	// priority over reverse DNS.
	NameMap map[string]string `json:"name_map,omitempty"`

	// SshdUnit is the systemd unit whose journal is followed for SSH auth
	// records ("ssh" on Debian/Ubuntu, "sshd" on RHEL); Linux only.
	SshdUnit string `json:"sshd_unit,omitempty"`

	// DataDir stores agent state: the self-signed TLS certificate.
	DataDir string `json:"data_dir,omitempty"`

	// LogLevel is slog level name: "debug", "info", "warn" or "error".
	LogLevel string `json:"log_level,omitempty"`

	// LogFile is the agent's own log path; empty logs to stderr.
	LogFile string `json:"log_file,omitempty"`

	// Health configures the disk health watch; nil means DefaultHealth.
	Health *Health `json:"health,omitempty"`
}

// Load reads and validates the configuration file at path. Missing optional
// fields receive defaults (aggregate window 300s, data dir "data", log level
// "info"); invalid values make Load return an error, never a partially
// usable Config. A UTF-8 BOM is tolerated: Windows editors add it even to
// otherwise plain JSON files.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save writes cfg to path atomically: the same validation as Load runs
// first, then the file is written to a temporary sibling and renamed, so a
// crash mid-write never leaves a broken config behind. The written file is
// loadable by Load without losses (indented JSON, no BOM).
func Save(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("save config: nil config")
	}
	// Validate a deep copy with defaults applied, so Save never mutates
	// the caller's struct, yet the checks see the effective values.
	check := *cfg
	check.applyDefaults()
	if err := check.validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	raw = append(raw, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// applyDefaults fills optional fields left empty by the file.
func (c *Config) applyDefaults() {
	if c.AggregateWindowSeconds == 0 {
		c.AggregateWindowSeconds = 300
	}
	if c.DataDir == "" {
		c.DataDir = "data"
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.SshdUnit == "" {
		c.SshdUnit = "ssh"
	}
	if c.Health == nil {
		c.Health = DefaultHealth()
	}
	if c.Profile != "" && len(c.Traps) == 0 {
		c.Traps = defaultTraps(c.Profile)
	}
}

// validate checks every field; the first problem found is reported.
func (c *Config) validate() error {
	if c.AgentName == "" {
		return fmt.Errorf("config: agent_name is required")
	}
	switch c.Profile {
	case "", ProfileDesktop, ProfileServer:
	default:
		return fmt.Errorf("config: unknown profile %q (want %q or %q)",
			c.Profile, ProfileDesktop, ProfileServer)
	}
	if len(c.Traps) == 0 {
		return fmt.Errorf("config: no traps configured (set traps or profile)")
	}
	seen := make(map[int]bool, len(c.Traps))
	for i, t := range c.Traps {
		if t.Port < 1 || t.Port > 65535 {
			return fmt.Errorf("config: traps[%d]: port %d out of range 1..65535", i, t.Port)
		}
		if seen[t.Port] {
			return fmt.Errorf("config: traps[%d]: duplicate port %d", i, t.Port)
		}
		seen[t.Port] = true
		if !knownEmulator(t.Emulator) {
			return fmt.Errorf("config: traps[%d] (port %d): unknown emulator %q", i, t.Port, t.Emulator)
		}
	}
	if c.Telegram.Token == "" {
		return fmt.Errorf("config: telegram.token is required")
	}
	if c.Telegram.ChatID == "" {
		return fmt.Errorf("config: telegram.chat_id is required")
	}
	if c.AggregateWindowSeconds <= 0 {
		return fmt.Errorf("config: aggregate_window_seconds must be positive, got %d", c.AggregateWindowSeconds)
	}
	for i, entry := range c.Allowlist {
		if _, _, err := ParseAllowlistEntry(entry); err != nil {
			return fmt.Errorf("config: allowlist[%d]: %s", i, err)
		}
	}
	if h := c.Health; h != nil {
		if h.CheckHour < 0 || h.CheckHour > 23 {
			return fmt.Errorf("config: health.check_hour must be 0..23, got %d", h.CheckHour)
		}
		if h.ReportWeekday < 0 || h.ReportWeekday > 6 {
			return fmt.Errorf("config: health.report_weekday must be 0..6 (time.Weekday), got %d", h.ReportWeekday)
		}
		if h.WarnPct < 1 || h.WarnPct > 99 {
			return fmt.Errorf("config: health.warn_pct must be 1..99, got %d", h.WarnPct)
		}
		if h.MinFreeGB < 0 {
			return fmt.Errorf("config: health.min_free_gb must be >= 0, got %d", h.MinFreeGB)
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: log_level must be debug, info, warn or error, got %q", c.LogLevel)
	}
	return nil
}

func knownEmulator(name string) bool {
	switch name {
	case EmulatorSilent, EmulatorHTTP, EmulatorFTP, EmulatorTelnet, EmulatorVNC,
		EmulatorMySQL, EmulatorPostgres, EmulatorSSH, EmulatorRDP:
		return true
	}
	return false
}

// ProfileTraps exposes the default trap set of a role profile to callers
// outside the package (the control UI marks profile ports as non-custom).
func ProfileTraps(profile string) []Trap { return defaultTraps(profile) }

// defaultTraps returns the port set for a role profile. Every port listed is
// expected to be free on a machine of that role; occupied ports are skipped
// by the trap manager at runtime.
func defaultTraps(profile string) []Trap {
	trap := func(port int, emu string) Trap {
		return Trap{Port: port, Enabled: true, Emulator: emu}
	}
	// Ports worth watching on any role: legacy or never-legitimate services.
	common := []Trap{
		trap(21, EmulatorFTP),
		trap(23, EmulatorTelnet),
		trap(69, EmulatorSilent),
		trap(2049, EmulatorSilent),
		trap(3306, EmulatorMySQL),
		trap(5432, EmulatorPostgres),
		trap(1433, EmulatorSilent),
		trap(6379, EmulatorSilent),
		trap(27017, EmulatorSilent),
		trap(5900, EmulatorVNC),
		trap(5985, EmulatorSilent),
		trap(5986, EmulatorSilent),
		trap(22, EmulatorSSH),
		trap(3389, EmulatorRDP),
	}
	switch profile {
	case ProfileDesktop:
		// On a workstation inbound 80/443 is an anomaly, so they are traps.
		http := trap(80, EmulatorHTTP)
		https := trap(443, EmulatorHTTP)
		https.TLS = true
		return append(common, http, https)
	case ProfileServer:
		// On a server 80/443 often serve legitimate web UIs: no traps there.
		return common
	}
	return nil
}
