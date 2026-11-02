package health

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StatePath returns the path of the persisted snapshot inside dataDir.
func StatePath(dataDir string) string {
	return filepath.Join(dataDir, "health_state.json")
}

// SaveState atomically persists snap to dataDir/health_state.json: the file
// is written to a temporary sibling and renamed, so a crash mid-write never
// leaves a broken state behind (same scheme as config.Save). The data
// directory is created on demand: a fresh install (e.g. a Linux kit without
// a pre-made data dir) must not lose its snapshot just for a missing
// directory.
func SaveState(dataDir string, snap Snapshot) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("save health state: create data dir: %w", err)
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("save health state: %w", err)
	}
	raw = append(raw, '\n')

	path := StatePath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("save health state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save health state: %w", err)
	}
	return nil
}

// LoadState reads the persisted snapshot. A missing file is not an error:
// it simply means "no previous snapshot" (first run), reported by ok=false
// with a nil snapshot. A corrupt file is reported as an error rather than
// silently treated as absent, so the operator notices the lost history.
func LoadState(dataDir string) (Snapshot, bool, error) {
	raw, err := os.ReadFile(StatePath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{}, false, nil
		}
		return Snapshot{}, false, fmt.Errorf("load health state: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return Snapshot{}, false, fmt.Errorf("load health state: %w", err)
	}
	return snap, true, nil
}
