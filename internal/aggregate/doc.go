// Package aggregate collapses bursts of similar events (brute force, port
// scans) into a first notification plus periodic digests, keeping Telegram
// delivery within API rate limits without dropping events.
package aggregate
