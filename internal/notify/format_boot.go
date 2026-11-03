package notify

import (
	"fmt"
	"strings"
	"time"
)

// BootFacts carries what the boot sensor learned about the machine
// lifecycle. Known marks a successful classification; when it is false the
// alert degrades to level 1 (the fact of the boot only) instead of
// disappearing. Initiator is best-effort: the platform source may not
// provide it, an empty value never breaks the alert.
type BootFacts struct {
	Known     bool      // classification of the previous session succeeded
	Clean     bool      // previous session ended with a proper shutdown
	Action    string    // "reboot", "shutdown", "" when undetermined
	Initiator string    // "user (process, «причина»)" when the source provides it
	LastAt    time.Time // end of the previous session (clean) / last activity (dirty); zero = unknown
	Now       time.Time // current boot moment
}

// BootAlertText renders the boot alert for the direct delivery channel
// (immediate, bypassing aggregation). Style follows the other alerts:
// machine header with the agent address, explicit marking of unknowns
// instead of omissions.
func BootAlertText(machine, ip string, f BootFacts) string {
	var b strings.Builder
	head := machine
	if ip != "" {
		head = fmt.Sprintf("%s (%s)", machine, ip)
	}
	fmt.Fprintf(&b, "🔔 %s: %s\n", head, bootHeadline(f))
	fmt.Fprintf(&b, "время загрузки: %s\n", f.Now.Format("2006-01-02 15:04:05"))
	if !f.Known {
		b.WriteString("детали прошлой сессии недоступны\n")
		return b.String()
	}
	if f.Clean {
		b.WriteString("прошлая сессия: завершена штатно")
		if !f.LastAt.IsZero() {
			fmt.Fprintf(&b, " в %s", f.LastAt.Format("02.01.2006 15:04:05"))
		}
		b.WriteString("\n")
		if f.Initiator != "" {
			fmt.Fprintf(&b, "инициатор: %s\n", f.Initiator)
		}
		if !f.LastAt.IsZero() {
			fmt.Fprintf(&b, "простой: %s\n", formatDowntime(f.LastAt, f.Now))
		}
	} else {
		b.WriteString("прошлая сессия: АВАРИЙНОЕ завершение (записей о штатном выключении нет — потеря питания / BSOD / паника?)\n")
		if !f.LastAt.IsZero() {
			fmt.Fprintf(&b, "последняя активность: %s\n", f.LastAt.Format("02.01.2006 15:04:05"))
		}
	}
	return b.String()
}

// bootHeadline names the event: a reboot, a power-on after an shutdown, or
// the bare fact when the action is undetermined.
func bootHeadline(f BootFacts) string {
	switch {
	case !f.Known:
		return "машина загрузилась"
	case f.Action == "reboot":
		return "машина ПЕРЕЗАГРУЗИЛАСЬ"
	case f.Action == "shutdown":
		return "машина включена после ВЫКЛЮЧЕНИЯ"
	}
	return "машина загрузилась"
}

// formatDowntime renders the gap between sessions in hours and minutes;
// negative gaps (clock skew) clamp to zero.
func formatDowntime(from, to time.Time) string {
	d := to.Sub(from)
	if d < 0 {
		d = 0
	}
	if h := int(d.Hours()); h > 0 {
		return fmt.Sprintf("%d ч %d мин", h, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d мин", int(d.Minutes()))
}
