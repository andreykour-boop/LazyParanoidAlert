//go:build windows

package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestNetshFirewallArgs(t *testing.T) {
	exe := `C:\Program Files\siren\siren.exe`

	add := netshFirewallArgs("add", exe)
	wantAdd := []string{"advfirewall", "firewall", "add", "rule",
		"name=" + firewallRuleName,
		"dir=in", "action=allow", "protocol=TCP", "program=" + exe}
	if !reflect.DeepEqual(add, wantAdd) {
		t.Errorf("add args = %v, want %v", add, wantAdd)
	}

	// delete and show address the rule by name only: cleanup removes
	// every rule with this name and the probe sees what exists.
	for _, action := range []string{"delete", "show"} {
		args := netshFirewallArgs(action, exe)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "name="+firewallRuleName) {
			t.Errorf("%s args miss the rule name: %v", action, args)
		}
		if strings.Contains(joined, "program=") {
			t.Errorf("%s args must not carry a program qualifier: %v", action, args)
		}
	}

	if got := netshFirewallArgs("bogus", exe); got != nil {
		t.Errorf("unknown action = %v, want nil", got)
	}
}

// TestFirewallWarnLifecycle: a failed ensure surfaces in the panel-side
// warn slot (the GUI has no stdout), a success clears it.
func TestFirewallWarnLifecycle(t *testing.T) {
	orig := netshRunner
	defer func() { netshRunner = orig }()

	netshRunner = func(args []string) ([]byte, error) {
		if strings.HasPrefix(strings.Join(args, " "), "advfirewall firewall add") {
			return []byte("denied"), fmt.Errorf("exit status 5")
		}
		return nil, fmt.Errorf("no rules match") // show: rule absent
	}
	exe := `C:\Program Files\siren\siren.exe`
	if err := firewallEnsure(exe); err == nil {
		t.Fatal("failing add must return an error")
	}
	if msg := firewallWarn.Load(); msg == nil || *msg == "" {
		t.Error("failure must set the panel warn message")
	}

	netshRunner = func(args []string) ([]byte, error) { return []byte("Ok."), nil }
	if err := firewallEnsure(exe); err != nil {
		t.Fatalf("successful add: %v", err)
	}
	if msg := firewallWarn.Load(); msg != nil && *msg != "" {
		t.Errorf("success must clear the warn message, got %q", *msg)
	}
}

func TestRulePointsToProgram(t *testing.T) {
	exe := `C:\Program Files\siren\siren.exe`
	// Locale-independent: match the path, not the field name. A real
	// English show-rule output looks like this; the localized field
	// names differ but the program path is echoed as given at add time.
	out := []byte("Rule Name:                            siren traps\r\n" +
		"Enabled:                              Yes\r\n" +
		"Program:                              " + exe + "\r\n")

	if !rulePointsToProgram(out, exe) {
		t.Error("output with the exe path must match")
	}
	if !rulePointsToProgram(out, strings.ToUpper(exe[:4])+exe[4:]) {
		t.Error("path comparison must be case-insensitive")
	}
	other := []byte("Rule Name: siren traps\r\nProgram: C:\\old\\path\\siren.exe\r\n")
	if rulePointsToProgram(other, exe) {
		t.Error("rule pointing at another path must not match")
	}
	if rulePointsToProgram(nil, exe) {
		t.Error("empty output must not match")
	}
}
