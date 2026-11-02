// Package doors collects authentication facts on ports occupied by real
// services: failed and successful logins for RDP on Windows (Security events
// 4624/4625, RemoteConnectionManager event 1149) and SSH on Linux (journald
// records of the ssh unit). Every fact carries the source IP, account and
// time when available.
package doors
