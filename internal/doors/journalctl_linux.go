//go:build linux

package doors

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"sync"

	"log/slog"

	"siren/internal/event"
)

// journalStream follows `journalctl -f -o json -u <unit>` on a Linux host.
type journalStream struct {
	cmd       *exec.Cmd
	stdout    io.ReadCloser
	scanner   *bufio.Scanner
	closeOnce sync.Once
}

// newJournalStreamFactory returns a streamFactory for the given sshd unit,
// e.g. "ssh" (Debian/Ubuntu) or "sshd" (RHEL). `-n 0` skips the last-ten
// records dump, so a restart never replays already seen logins.
func newJournalStreamFactory(unit string) streamFactory {
	return func(ctx context.Context) (lineStream, error) {
		cmd := exec.CommandContext(ctx, "journalctl", "-f", "-o", "json", "-u", unit, "-n", "0")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(stdout)
		// Journal records with big MESSAGE payloads stay under 1 MiB.
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		return &journalStream{cmd: cmd, stdout: stdout, scanner: sc}, nil
	}
}

// Next implements lineStream.
func (j *journalStream) Next() (string, error) {
	if !j.scanner.Scan() {
		if err := j.scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF // journalctl exited
	}
	return j.scanner.Text(), nil
}

// Close implements lineStream: terminates journalctl and waits for it, so
// no child process is left behind on restart or shutdown.
func (j *journalStream) Close() error {
	var err error
	j.closeOnce.Do(func() {
		_ = j.stdout.Close()
		err = j.cmd.Wait()
	})
	return err
}

// JournaldSource follows sshd authentication records on a Linux host and
// pushes agent events into Events. Unrecognized sshd messages are recorded
// at debug level for parser diagnostics, never silently dropped.
type JournaldSource struct {
	// Unit is the sshd systemd unit name: "ssh" on Debian/Ubuntu,
	// "sshd" on RHEL.
	Unit    string
	Machine string
	Events  chan<- event.Event
	Log     *slog.Logger
}

// Run follows the journal until ctx is done. journalctl deaths are handled
// inside runStreams with restart backoff.
func (s *JournaldSource) Run(ctx context.Context) error {
	runStreams(ctx, s.Log, newJournalStreamFactory(s.Unit), func(line string) {
		ev, ok, err := journalLineToEvent(line, s.Machine)
		if err != nil {
			s.Log.Warn("doors: journal record skipped", "err", err)
			return
		}
		if !ok {
			// An sshd message that is not an auth fact; the spec requires
			// keeping its text for parser tuning, never a silent drop.
			s.Log.Debug("doors: unmatched sshd message", "line", line)
			return
		}
		select {
		case s.Events <- ev:
		case <-ctx.Done():
		}
	})
	return ctx.Err()
}
