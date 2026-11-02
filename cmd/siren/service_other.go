//go:build !windows

package main

import "fmt"

// isServiceMode is a non-Windows stub: there is no SCM outside Windows.
func isServiceMode() bool { return false }

// runService is a non-Windows stub.
func runService(string) error {
	return fmt.Errorf("siren: service mode is only supported on Windows")
}

// serviceCommand is a non-Windows stub.
func serviceCommand(cmd, cfgPath, account, password string) error {
	return fmt.Errorf("siren: -service is only supported on Windows")
}
