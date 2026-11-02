//go:build windows

package main

import (
	"context"

	"log/slog"

	"siren/internal/config"
	"siren/internal/doors"
	"siren/internal/event"
)

// runDoors starts the Windows authentication sensors: the Security log for
// 4624/4625 (Logon Type 10) and the RemoteConnectionManager for 1149. A
// denied Security subscription (no Event Log Readers membership) is skipped
// inside the source with a warning.
func runDoors(ctx context.Context, cfg *config.Config, events chan<- event.Event, log *slog.Logger) error {
	source := doors.NewWinSource(cfg.AgentName, events, log)
	return source.Run(ctx)
}
