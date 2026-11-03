//go:build windows

package doors

import (
	"strings"
	"testing"
)

// TestQueryEventsXMLSystemChannel is a live smoke test: every Windows
// machine has a System channel and every boot leaves an event-log start
// (6005) record, so the query must return at least one rendered event.
func TestQueryEventsXMLSystemChannel(t *testing.T) {
	xmls, err := QueryEventsXML("System", "*[System[(EventID=6005 or EventID=6006)]]", 5)
	if err != nil {
		t.Fatalf("query System channel: %v", err)
	}
	if len(xmls) == 0 {
		t.Fatal("no event-log start/stop records found; System channel is never empty on a booted machine")
	}
	for _, x := range xmls {
		if !strings.Contains(x, "<Event") {
			t.Errorf("rendered record is not an event XML: %.100s", x)
		}
	}
}
