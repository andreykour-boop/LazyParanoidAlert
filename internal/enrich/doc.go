// Package enrich augments raw events with source identity data: reverse DNS
// hostname, MAC address from the ARP table and the manual IP-to-name map from
// the configuration, which takes priority over DNS. Lookups are cached with a
// TTL.
package enrich
