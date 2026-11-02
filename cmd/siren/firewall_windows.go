//go:build windows

package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
)

// The firewall side of the service lifecycle: traps must accept inbound
// connections from other machines, so installing the service ensures one
// program-scoped allow rule, and removing the service drops it. The rule
// is bound to the agent executable, not to ports: the port set lives in
// the config and may change without touching the firewall.

// firewallRuleName is the display name of the program-scoped allow rule.
const firewallRuleName = "siren traps"

// netshFirewallArgs builds the netsh argument vector for a firewall
// action. "add" binds the rule to exe; "delete" and "show" address the
// rule by name only, so cleanup removes every rule with this name and
// the probe sees what actually exists. Arguments are passed as separate
// argv entries: os/exec quotes paths with spaces, no manual quoting.
func netshFirewallArgs(action, exe string) []string {
	switch action {
	case "add":
		return []string{"advfirewall", "firewall", "add", "rule",
			"name=" + firewallRuleName,
			"dir=in", "action=allow", "protocol=TCP", "program=" + exe}
	case "delete":
		return []string{"advfirewall", "firewall", "delete", "rule",
			"name=" + firewallRuleName}
	case "show":
		return []string{"advfirewall", "firewall", "show", "rule",
			"name=" + firewallRuleName}
	}
	return nil
}

// rulePointsToProgram reports whether netsh "show rule" output describes
// a rule whose program is exe. netsh output is localized, so field names
// cannot be relied on: matching the executable path inside the output is
// locale-independent. Comparison is case-insensitive (Windows paths).
func rulePointsToProgram(output []byte, exe string) bool {
	return strings.Contains(strings.ToLower(string(output)), strings.ToLower(exe))
}

// netshRunner runs netsh with the given arguments; a package var so
// tests can fake failures without touching the real firewall.
var netshRunner = func(args []string) ([]byte, error) {
	return exec.Command("netsh", args...).CombinedOutput()
}

// firewallWarn carries the latest rule-ensure failure to the control
// panel: the CLI prints notes to stdout, the GUI has no stdout.
var firewallWarn atomic.Pointer[string]

// firewallEnsure makes the rule point at exe: absent rule is created, a
// rule for a previous install location is replaced, an up-to-date rule
// is left alone (idempotent re-install).
func firewallEnsure(exe string) error {
	var err error
	defer func() {
		if err != nil {
			msg := err.Error()
			firewallWarn.Store(&msg)
		} else {
			firewallWarn.Store(nil)
		}
	}()

	out, err := netshRunner(netshFirewallArgs("show", ""))
	if err == nil && rulePointsToProgram(out, exe) {
		return nil
	}
	if err == nil {
		// The rule exists but points elsewhere (agent moved): replace it.
		_, _ = netshRunner(netshFirewallArgs("delete", ""))
	}
	err = firewallAdd(exe)
	return err
}

// firewallAdd creates the allow rule for exe.
func firewallAdd(exe string) error {
	out, err := netshRunner(netshFirewallArgs("add", exe))
	if err != nil {
		return fmt.Errorf("netsh add rule: %v: %s", err, string(out))
	}
	return nil
}

// firewallRemove drops the rule. A missing rule is fine (nothing to
// clean up); any other failure is reported to the caller, which decides
// how loud to be. Best-effort by contract.
func firewallRemove() error {
	out, err := netshRunner(netshFirewallArgs("delete", ""))
	if err != nil {
		// netsh reports a missing rule with a non-zero exit and a
		// localized message; the safest locale-independent marker we
		// have is the absence of our rule name in the output.
		if !bytes.Contains(out, []byte(firewallRuleName)) {
			return nil
		}
		return fmt.Errorf("netsh delete rule: %v: %s", err, string(out))
	}
	return nil
}
