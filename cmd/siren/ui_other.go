//go:build !windows

package main

// runInteractive is the non-Windows fallback: there is no control UI
// outside Windows, so an interactive launch runs the console pipeline.
func runInteractive(cfgPath string) error {
	return runConsole(cfgPath)
}

// initConsole is a non-Windows stub: console output is native there.
func initConsole() {}
