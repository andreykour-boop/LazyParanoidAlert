package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Allowlist entry format: "IP[/CIDR]" with an optional ":port[,port…]"
// suffix (ports 1–65535); an IPv6 address with ports uses the bracketed
// form "[::1]:22". An entry without ports covers every port; with ports it
// covers only the listed ones.

// ParseAllowlistEntry splits an allowlist entry into its address part and
// the optional port list. The whole string is tried as IP/CIDR first — the
// legacy format and unbracketed IPv6 ("::1", "fe80::1:22") stay plain
// addresses; only on that miss is the suffix after the last ':' parsed as
// a port list, with the head re-checked as IP/CIDR (an IPv6 head must be
// bracketed to be unambiguous). One parser serves the config validation,
// the runtime matcher and the control UI (design D4).
func ParseAllowlistEntry(s string) (addr string, ports []int, err error) {
	s = strings.TrimSpace(s)
	if validIPorCIDR(s) {
		return s, nil, nil
	}
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", nil, fmt.Errorf("%q is not an IP, CIDR or host:ports entry", s)
	}
	head, tail := s[:i], s[i+1:]
	if strings.HasPrefix(head, "[") && strings.HasSuffix(head, "]") {
		head = head[1 : len(head)-1]
	}
	if !validIPorCIDR(head) {
		return "", nil, fmt.Errorf("%q is not an IP, CIDR or host:ports entry", s)
	}
	for _, p := range strings.Split(tail, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 1 || n > 65535 {
			return "", nil, fmt.Errorf("%q is not an IP, CIDR or host:ports entry", s)
		}
		ports = append(ports, n)
	}
	if len(ports) == 0 {
		return "", nil, fmt.Errorf("%q is not an IP, CIDR or host:ports entry", s)
	}
	return head, ports, nil
}
