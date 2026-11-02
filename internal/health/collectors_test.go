package health

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindToolCandidates: after a PATH miss, findTool probes the injected
// well-known locations and returns the first existing file; without
// candidates a missing tool stays "not installed" (empty string).
func TestFindToolCandidates(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "smartctl.exe")
	if err := os.WriteFile(existing, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := toolCandidates
	defer func() { toolCandidates = orig }()

	toolCandidates = func(name string) []string {
		return []string{filepath.Join(dir, "missing", "smartctl.exe"), existing}
	}
	if got := findTool("definitely-not-in-path-xyz"); got != existing {
		t.Errorf("findTool = %q, want %q (first existing candidate)", got, existing)
	}

	toolCandidates = func(name string) []string { return nil }
	if got := findTool("definitely-not-in-path-xyz"); got != "" {
		t.Errorf("findTool without candidates = %q, want empty", got)
	}
}
