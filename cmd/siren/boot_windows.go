//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"siren/internal/doors"
	"siren/internal/notify"
)

// Windows boot identity: LastBootUpTime of Win32_OperatingSystem, rendered
// as UTC RFC3339. A PowerShell CIM one-shot is the established fallback
// pattern of this project (disks_windows.go uses the same); the query runs
// once per agent start, its latency is irrelevant at that moment.
func init() { bootIdentityFn = windowsBootIdentity }

// windowsBootClassify reads shutdown fingerprints of the previous session
// from the System channel (D3): 1074 (USER32 — initiator, process, reason,
// action), 6006 (clean EventLog stop), 6008 (unexpected shutdown), 41
// (Kernel-Power, dirty). The System channel needs no elevation.
func init() { bootClassifyFn = windowsBootClassify }

func windowsBootIdentity() (string, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`(Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToString("o")`).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("boot: query LastBootUpTime: %v: %s", err, strings.TrimSpace(string(out)))
	}
	s := strings.TrimSpace(string(out))
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", fmt.Errorf("boot: parse LastBootUpTime %q: %v", s, err)
	}
	return t.UTC().Format(time.RFC3339), nil
}

const (
	bootQueryChannel = "System"
	// Shutdown fingerprints of the previous session, 30 days back. The
	// current-session events are cut off in Go against the boot time.
	bootQuery      = `*[System[(EventID=1074 or EventID=6006 or EventID=6008 or EventID=41) and TimeCreated[timediff(@SystemTime) <= 2592000000]]]`
	bootQueryLimit = 200
)

// windowsBootClassify: current boot time as the cutoff, historical query,
// pure classification of the previous session. Any failure surfaces to the
// caller which degrades the alert to level 1.
func windowsBootClassify() (notify.BootFacts, error) {
	id, err := windowsBootIdentity()
	if err != nil {
		return notify.BootFacts{}, fmt.Errorf("boot: classify: %v", err)
	}
	bootTime, err := time.Parse(time.RFC3339, id)
	if err != nil {
		return notify.BootFacts{}, fmt.Errorf("boot: classify: parse boot time: %v", err)
	}
	xmls, err := doors.QueryEventsXML(bootQueryChannel, bootQuery, bootQueryLimit)
	if err != nil {
		return notify.BootFacts{}, fmt.Errorf("boot: classify: %w", err)
	}
	return classifyWindowsSession(bootPrevSessionEvents(xmls, bootTime), time.Now()), nil
}

// bootWinEvent is one parsed System-channel record; Params keeps the
// EventData values keyed by their names (param1..paramN).
type bootWinEvent struct {
	ID     int
	At     time.Time
	Params map[string]string
}

// bootPrevSessionEvents parses raw XML records and keeps only those from
// before bootTime (the previous session); malformed records are skipped,
// never failing the alert.
func bootPrevSessionEvents(xmls []string, bootTime time.Time) []bootWinEvent {
	var events []bootWinEvent
	for _, raw := range xmls {
		id, at, data, err := doors.ParseEventXML(raw)
		if err != nil {
			continue
		}
		if at.Before(bootTime) {
			params := make(map[string]string, len(data))
			for k, v := range data {
				params[k] = strings.TrimSpace(v)
			}
			events = append(events, bootWinEvent{ID: id, At: at, Params: params})
		}
	}
	return events
}

// classifyWindowsSession builds the alert facts from previous-session
// records (newest-first is enforced here). Clean means a 6006 (event log
// stopped properly) or a 1074 (an initiated shutdown/restart) exists; dirty
// sessions keep the freshest record time as the last activity. The 1074
// params are localized (Russian/English), so initiator extraction is
// heuristic by shape rather than by field index.
func classifyWindowsSession(events []bootWinEvent, now time.Time) notify.BootFacts {
	sort.Slice(events, func(i, j int) bool { return events[i].At.After(events[j].At) })
	facts := notify.BootFacts{Now: now}
	var user, proc, reason, action string
	var saw1074 bool
	for _, ev := range events {
		switch ev.ID {
		case 1074:
			if action == "" {
				action = bootEventAction(ev.Params)
			}
			if user == "" {
				user = bootEventUser(ev.Params)
			}
			if proc == "" {
				proc = bootEventProcess(ev.Params)
			}
			if reason == "" {
				reason = bootEventReason(ev.Params)
			}
			saw1074 = true
		case 6006:
			facts.Clean = true
		}
	}
	if saw1074 {
		facts.Clean = true
	}
	facts.Known = true
	facts.Action = action
	if facts.Clean && facts.Action == "" {
		// A clean stop without a 1074 record: every initiated restart goes
		// through USER32, so a silent proper stop is a plain power-off.
		facts.Action = "shutdown"
	}
	if facts.Clean {
		for _, ev := range events { // newest first: the shutdown moment
			if ev.ID == 1074 || ev.ID == 6006 {
				facts.LastAt = ev.At
				break
			}
		}
		facts.Initiator = bootInitiator(user, proc, reason)
	} else if len(events) > 0 {
		facts.LastAt = events[0].At // last trace of the dead session
	}
	return facts
}

// bootEventUser finds an account value (DOMAIN\user) among the params,
// excluding paths (contain ":\\"), executables and long reason texts.
func bootEventUser(params map[string]string) string {
	for _, k := range []string{"param4", "param5"} {
		if v := params[k]; isBootUserValue(v) {
			return v
		}
	}
	for _, v := range params {
		if isBootUserValue(v) {
			return v
		}
	}
	return ""
}

func isBootUserValue(v string) bool {
	return v != "" && strings.Contains(v, "\\") &&
		!strings.Contains(v, ":\\") && !strings.Contains(v, ".exe") &&
		len(v) < 128
}

// bootEventProcess extracts the process name from the "C:\...\proc.exe
// (COMPUTER)" param.
func bootEventProcess(params map[string]string) string {
	for _, v := range params {
		if !strings.Contains(v, ".exe") {
			continue
		}
		s := v
		if i := strings.Index(s, " ("); i >= 0 {
			s = s[:i]
		}
		if j := strings.LastIndexAny(s, `\/`); j >= 0 {
			s = s[j+1:]
		}
		return s
	}
	return ""
}

// bootEventAction names the initiated action: reboot or shutdown. The
// keyword set covers Russian and English USER32 phrasings.
func bootEventAction(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lv := strings.ToLower(params[k])
		switch {
		case strings.Contains(lv, "перезагруз") ||
			strings.Contains(lv, "restart") || strings.Contains(lv, "reboot"):
			return "reboot"
		case strings.Contains(lv, "завершение работы") || strings.Contains(lv, "выключ") ||
			strings.Contains(lv, "shut") || strings.Contains(lv, "power"):
			return "shutdown"
		}
	}
	return ""
}

// bootEventReason picks the shutdown reason text: the first non-empty
// trailing param that is not an account or a process path.
func bootEventReason(params map[string]string) string {
	for _, k := range []string{"param5", "param6", "param7"} {
		v := params[k]
		if v == "" || isBootUserValue(v) || strings.Contains(v, ".exe") {
			continue
		}
		return v
	}
	return ""
}

// bootInitiator renders "user (proc, «reason»)"; empty parts are skipped,
// an empty result leaves the whole initiator line out of the alert.
func bootInitiator(user, proc, reason string) string {
	if user == "" && proc == "" && reason == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(user)
	if proc != "" {
		b.WriteString(" (" + proc)
		if reason != "" {
			b.WriteString(", «" + reason + "»")
		}
		b.WriteString(")")
	} else if reason != "" {
		if user != "" {
			b.WriteString(", ")
		}
		b.WriteString("«" + reason + "»")
	}
	return b.String()
}
