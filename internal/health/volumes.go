package health

import "fmt"

// volumesSource collects mounted local filesystems. Mount enumeration and
// the statfs bridge are injectable, so filtering logic is testable on any
// platform; the real wiring lives in the linux file.
type volumesSource struct {
	// mounts returns the candidate mount entries (already parsed from
	// /proc/mounts or the platform equivalent).
	mounts func() ([]mountEntry, error)
	// statfs fills total/free byte counts for a mount point.
	statfs func(path string) (total, free uint64, err error)
}

func (s *volumesSource) Name() string { return "volumes" }

func (s *volumesSource) CollectVolumes() ([]Volume, error) {
	if s.mounts == nil || s.statfs == nil {
		return nil, fmt.Errorf("volumes source not configured")
	}
	entries, err := s.mounts()
	if err != nil {
		return nil, err
	}
	vols := make([]Volume, 0, len(entries))
	for _, m := range entries {
		total, free, err := s.statfs(m.MountPoint)
		if err != nil {
			// A mount that vanished mid-scan (unmounted, stale NFS) is not
			// fatal: skip it; the report still lists the rest.
			continue
		}
		if total == 0 {
			continue
		}
		vols = append(vols, Volume{Mount: m.MountPoint, TotalBytes: total, FreeBytes: free})
	}
	return vols, nil
}
