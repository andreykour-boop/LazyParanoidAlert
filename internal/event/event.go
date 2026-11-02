// Package event defines the single event type exchanged between agent
// components: trap connections and authentication facts flow through it
// towards enrichment, aggregation and delivery.
package event

import (
	"net/netip"
	"time"
)

// Kind classifies an event; values appear in Telegram alerts, so they stay
// short and stable.
type Kind string

const (
	// KindTrapConnect is a connection accepted by a decoy listener.
	KindTrapConnect Kind = "trap_connect"
	// KindAuthAttempt is a failed authentication on a real service.
	KindAuthAttempt Kind = "auth_attempt"
	// KindAuthLogin is a successful authentication on a real service.
	KindAuthLogin Kind = "auth_login"
	// KindDiskHealthAlert is an immediate disk-health alarm (SMART or
	// array status change, lost data source) from the health watch.
	KindDiskHealthAlert Kind = "disk_health_alert"
	// KindDiskSpaceAlert is a free-space threshold violation alarm from
	// the health watch.
	KindDiskSpaceAlert Kind = "disk_space_alert"
)

// Event is one observed fact. SrcIP is the zero value when the source address
// is unknown (some Windows auth events carry no IP); consumers treat such
// events as "source unknown" rather than dropping them. Account, Hostname and
// MAC are empty when not yet known.
type Event struct {
	Kind     Kind
	Time     time.Time
	Machine  string     // agent_name from the config
	Protocol string     // trap emulator name, or "ssh"/"rdp" for auth facts
	SrcIP    netip.Addr // zero value = source unknown
	SrcPort  int
	DstPort  int
	Account  string
	Password string // credentials tried against a decoy service; empty for real-service facts
	Hostname string
	MAC      string

	// Detail carries free text for health-watch alarms (the change list
	// or violating volumes); empty for regular events.
	Detail string
}
