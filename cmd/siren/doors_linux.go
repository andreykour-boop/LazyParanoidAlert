//go:build linux

package main

import (
	"context"

	"log/slog"

	"siren/internal/config"
	"siren/internal/doors"
	"siren/internal/event"
)

// runDoors follows the sshd journal for authentication records. The unit
// name comes from the config: "ssh" on Debian/Ubuntu, "sshd" on RHEL.
func runDoors(ctx context.Context, cfg *config.Config, events chan<- event.Event, log *slog.Logger) error {
	source := &doors.JournaldSource{
		Unit:    cfg.SshdUnit,
		Machine: cfg.AgentName,
		Events:  events,
		Log:     log,
	}
	return source.Run(ctx)
}
