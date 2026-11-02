// Package config loads and validates the agent's JSON configuration:
// per-port trap toggles and emulators, machine role profile, Telegram
// credentials and chat, aggregation window, source allowlist and the
// manual IP-to-name map.
//
// Configuration is validated at startup; Load returns an error for any
// invalid or incomplete setting rather than applying defaults silently.
package config
