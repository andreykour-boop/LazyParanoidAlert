package notify

import (
	"fmt"
	"strconv"
	"strings"

	"siren/internal/aggregate"
	"siren/internal/event"
)

// kindLabels are short Russian phrases for alert headers; they must stay
// stable because they are part of the notification format.
var kindLabels = map[event.Kind]string{
	event.KindTrapConnect:     "подключение к ловушке",
	event.KindAuthAttempt:     "ПОПЫТКА ВХОДА",
	event.KindAuthLogin:       "ВХОД",
	event.KindDiskHealthAlert: "ТРЕВОГА ДИСКОВ",
	event.KindDiskSpaceAlert:  "МЕСТО НА ИСХОДЕ",
}

// kindLabel returns the header phrase for a kind, with a fallback.
func kindLabel(k event.Kind) string {
	if s, ok := kindLabels[k]; ok {
		return s
	}
	return string(k)
}

// Formatter renders notices as plain Telegram text. AgentIP is the monitored
// machine's own address, shown next to the machine name so that alerts from
// several agents stay distinguishable. Unknown fields are marked explicitly
// ("имя не определено", "IP неизвестен") instead of being omitted, so the
// receiver can always tell what the sensor saw.
type Formatter struct {
	AgentIP string
}

// Format renders one notice. The package-level Format is kept for callers
// that do not know the agent address.
func Format(n aggregate.Notice) string { return Formatter{}.Format(n) }

// Format implements the formatter.
func (f Formatter) Format(n aggregate.Notice) string {
	ev := n.First
	var b strings.Builder

	head := ev.Machine
	if f.AgentIP != "" {
		head = fmt.Sprintf("%s (%s)", ev.Machine, f.AgentIP)
	}
	fmt.Fprintf(&b, "🚨 %s: %s", head, kindLabel(ev.Kind))
	if n.Digest {
		fmt.Fprintf(&b, " (+%d за %s)", n.Count-1, n.Window)
	}
	b.WriteByte('\n')

	fmt.Fprintf(&b, "время: %s\n", ev.Time.Format("2006-01-02 15:04:05"))

	// Source line: IP, hostname, MAC — each marked when unknown.
	var src strings.Builder
	if ev.SrcIP.IsValid() {
		src.WriteString(ev.SrcIP.String())
	} else {
		src.WriteString("IP неизвестен")
	}
	if ev.Hostname != "" {
		src.WriteString(" · " + ev.Hostname)
	} else {
		src.WriteString(" · имя не определено")
	}
	if ev.MAC != "" {
		src.WriteString(" · " + ev.MAC)
	}
	fmt.Fprintf(&b, "источник: %s\n", src.String())

	// Service line: destination port and protocol; traps report the
	// emulated service, auth facts report ssh/rdp.
	if ev.DstPort != 0 {
		fmt.Fprintf(&b, "порт: %d (%s)\n", ev.DstPort, ev.Protocol)
	} else if ev.Protocol != "" {
		fmt.Fprintf(&b, "порт: ? (%s)\n", ev.Protocol)
	}
	// The single-port line above shows only the first event of the window.
	// A digest over events with different destination ports (a port scan
	// hitting several traps) names them all.
	if n.Digest && len(n.Ports) > 1 {
		parts := make([]string, len(n.Ports))
		for i, p := range n.Ports {
			parts[i] = strconv.Itoa(p)
		}
		fmt.Fprintf(&b, "порты: %s\n", strings.Join(parts, ", "))
	}

	if ev.Account != "" {
		fmt.Fprintf(&b, "учётка: %s\n", ev.Account)
	}
	// Credentials typed against a decoy service (SSH trap). Real-service
	// facts never carry passwords: the OS does not expose them.
	if ev.Password != "" {
		fmt.Fprintf(&b, "пароль: %s\n", ev.Password)
	}
	// Health-watch alarms carry their change list as free text; printed
	// verbatim (it is already a human-readable multi-line body).
	if ev.Detail != "" {
		fmt.Fprintln(&b, ev.Detail)
	}
	if n.Digest && n.Count > 2 {
		fmt.Fprintf(&b, "последний повтор: %s\n", n.Last.Time.Format("2006-01-02 15:04:05"))
	}
	return strings.TrimRight(b.String(), "\n")
}
