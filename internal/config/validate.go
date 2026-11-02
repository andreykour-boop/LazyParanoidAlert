package config

import (
	"net/netip"
	"strings"
)

// validIPorCIDR reports whether s is a plain IP address or a CIDR network.
func validIPorCIDR(s string) bool {
	if strings.Contains(s, "/") {
		_, err := netip.ParsePrefix(s)
		return err == nil
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}
