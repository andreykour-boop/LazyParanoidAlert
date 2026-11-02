// Package health implements the disk health watch: collectors for the two
// layers (L2 arrays/logical volumes, L1 physical disks) and mounted volumes,
// snapshot diffing, on-disk state and the wall-clock scheduler/runner.
package health

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

// Status is the normalized health of a single entity.
type Status string

const (
	StatusOK      Status = "ok"
	StatusWarn    Status = "warn"
	StatusFail    Status = "fail"
	StatusUnknown Status = "unknown"
)

// L2 entity kinds: software RAID, a storage pool (Windows Storage Spaces or
// a hardware controller volume) and a logical volume behind such a pool.
const (
	KindMD     = "md"
	KindPool   = "pool"
	KindVolume = "lvolume"
)

// L2State is the state of an array/pool/logical volume.
type L2State string

const (
	L2Clean      L2State = "clean"
	L2Degraded   L2State = "degraded"
	L2Rebuilding L2State = "rebuilding"
	L2Failed     L2State = "failed"
	L2Unknown    L2State = "unknown"
)

// L2Entity is one array, storage pool or logical volume.
type L2Entity struct {
	Name   string  `json:"name"`
	Kind   string  `json:"kind"`
	State  L2State `json:"state"`
	Reason string  `json:"reason,omitempty"` // detail for degraded/failed/unknown
}

// L1Disk is one physical disk as seen by SMART or the platform collector.
// Realloc/Pending are the SMART reallocated- and pending-sector counts
// (0 when the source does not report them). MediaErrors/Wear are the NVMe
// health-log counters (media and data integrity errors, percentage used).
// CountersSrc names the source that delivered the counters ("smartctl",
// "wmi", ...): the diff re-baselines counters when the source changes,
// because the same counter name carries different semantics per source.
type L1Disk struct {
	Name        string `json:"name"`
	Model       string `json:"model,omitempty"`
	Serial      string `json:"serial,omitempty"`
	Status      Status `json:"status"`
	Reason      string `json:"reason,omitempty"` // detail for unknown
	Realloc     int64  `json:"realloc,omitempty"`
	Pending     int64  `json:"pending,omitempty"`
	MediaErrors int64  `json:"media_errors,omitempty"`
	Wear        int64  `json:"wear,omitempty"`
	CountersSrc string `json:"counters_src,omitempty"`
}

// Volume is one mounted filesystem with its capacity. Internally sizes are
// bytes; in the persisted state file they are written as GiB (two decimals)
// so the file reads naturally, and both the GiB and the legacy byte fields
// are accepted when loading.
type Volume struct {
	Mount      string `json:"mount"`
	TotalBytes uint64 `json:"-"`
	FreeBytes  uint64 `json:"-"`
	// ReadOnly marks network/removable media excluded from space alerts.
	ReadOnly bool `json:"read_only,omitempty"`
}

const gib = 1 << 30

func bytesToGB(b uint64) float64  { return math.Round(giB(b)*100) / 100 }
func gbToBytes(gb float64) uint64 { return uint64(math.Round(gb * gib)) }

// MarshalJSON renders sizes in GiB ("total_gb"/"free_gb") instead of raw
// bytes: the state file is read by humans too, and "218.99 ГБ" beats a
// 13-digit byte count.
func (v Volume) MarshalJSON() ([]byte, error) {
	type alias struct {
		Mount    string  `json:"mount"`
		TotalGB  float64 `json:"total_gb"`
		FreeGB   float64 `json:"free_gb"`
		ReadOnly bool    `json:"read_only,omitempty"`
	}
	return json.Marshal(alias{
		Mount:    v.Mount,
		TotalGB:  bytesToGB(v.TotalBytes),
		FreeGB:   bytesToGB(v.FreeBytes),
		ReadOnly: v.ReadOnly,
	})
}

// UnmarshalJSON accepts both the current GiB fields and the legacy
// "total_bytes"/"free_bytes", so a state file written by an older agent
// still loads as a diff base after an upgrade.
func (v *Volume) UnmarshalJSON(raw []byte) error {
	var f struct {
		Mount      string   `json:"mount"`
		TotalGB    *float64 `json:"total_gb"`
		FreeGB     *float64 `json:"free_gb"`
		TotalBytes *uint64  `json:"total_bytes"`
		FreeBytes  *uint64  `json:"free_bytes"`
		ReadOnly   bool     `json:"read_only"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	v.Mount = f.Mount
	v.ReadOnly = f.ReadOnly
	switch {
	case f.TotalGB != nil:
		v.TotalBytes = gbToBytes(*f.TotalGB)
	case f.TotalBytes != nil:
		v.TotalBytes = *f.TotalBytes
	}
	switch {
	case f.FreeGB != nil:
		v.FreeBytes = gbToBytes(*f.FreeGB)
	case f.FreeBytes != nil:
		v.FreeBytes = *f.FreeBytes
	}
	return nil
}

// FreePct returns free space as a percent of total capacity; 0 for empty
// volumes (total 0 is not expected from real collectors).
func (v Volume) FreePct() float64 {
	if v.TotalBytes == 0 {
		return 0
	}
	return float64(v.FreeBytes) / float64(v.TotalBytes) * 100
}

// SourceError records a data source that could not be collected and why:
// the "do not stay silent" rule requires naming it, never hiding it.
type SourceError struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// Snapshot is one full health reading of the machine.
type Snapshot struct {
	CollectedAt time.Time     `json:"collected_at"`
	L2          []L2Entity    `json:"l2,omitempty"`
	L1          []L1Disk      `json:"l1,omitempty"`
	Volumes     []Volume      `json:"volumes,omitempty"`
	Errors      []SourceError `json:"errors,omitempty"`
}

// L2Source collects array/pool/logical-volume entities. A failure to collect
// (tool missing, controller unreachable) is an error, not an empty list.
type L2Source interface {
	Name() string
	CollectL2() ([]L2Entity, error)
}

// L1Source collects physical disks.
type L1Source interface {
	Name() string
	CollectL1() ([]L1Disk, error)
}

// VolumesSource collects mounted volumes.
type VolumesSource interface {
	Name() string
	CollectVolumes() ([]Volume, error)
}

// Collector builds a Snapshot from registered sources. Sources that return
// an error contribute a SourceError instead of their entities, so a missing
// smartctl degrades to an honest "unknown" rather than silence.
type Collector struct {
	L2Sources      []L2Source
	L1Sources      []L1Source
	VolumesSources []VolumesSource

	// Now is used to stamp the snapshot; defaults to time.Now.
	Now func() time.Time
}

// Snapshot runs every registered source and assembles the reading. Entity
// lists are sorted by name, so identical inputs yield byte-stable snapshots
// (a prerequisite for reliable diffing).
func (c *Collector) Snapshot() Snapshot {
	snap := Snapshot{}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	snap.CollectedAt = now()

	for _, src := range c.L2Sources {
		entities, err := src.CollectL2()
		if err != nil {
			snap.Errors = append(snap.Errors, SourceError{Source: src.Name(), Reason: err.Error()})
			continue
		}
		snap.L2 = append(snap.L2, entities...)
	}
	for _, src := range c.L1Sources {
		disks, err := src.CollectL1()
		if err != nil {
			// An absent hardware-RAID vendor CLI on a machine with no RAID
			// arrays at all is a permanent note, not a finding: there is
			// nothing behind a controller it could check. Drop it at
			// collection time, so neither the state file nor any report
			// carries the noise; if arrays appear, the error is recorded
			// again and becomes visible everywhere.
			if src.Name() == "vendor-cli" && len(snap.L2) == 0 {
				continue
			}
			snap.Errors = append(snap.Errors, SourceError{Source: src.Name(), Reason: err.Error()})
			continue
		}
		snap.L1 = append(snap.L1, disks...)
	}
	for _, src := range c.VolumesSources {
		vols, err := src.CollectVolumes()
		if err != nil {
			snap.Errors = append(snap.Errors, SourceError{Source: src.Name(), Reason: err.Error()})
			continue
		}
		snap.Volumes = append(snap.Volumes, vols...)
	}

	sort.Slice(snap.L2, func(i, j int) bool { return snap.L2[i].Name < snap.L2[j].Name })
	sort.Slice(snap.L1, func(i, j int) bool { return snap.L1[i].Name < snap.L1[j].Name })
	sort.Slice(snap.Volumes, func(i, j int) bool { return snap.Volumes[i].Mount < snap.Volumes[j].Mount })
	return snap
}
