package main

import "net"

// resolveAgentIP returns the address of the primary outgoing interface: the
// source address the OS would pick for a default-route connection. It never
// sends a packet (UDP connect only) and falls back to an empty string when
// the machine is offline — the alert then shows just the machine name.
func resolveAgentIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	if udp, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return udp.IP.String()
	}
	return ""
}
