package health

import (
	"fmt"
	"strings"
)

// mdadmSource collects software RAID arrays: the array list comes from
// /proc/mdstat, the per-array detail from `mdadm --detail /dev/mdN`.
// Both dependencies are injectable for tests; the real wiring lives in the
// linux file.
type mdadmSource struct {
	// listArrays returns array device names ("md0", "md127").
	listArrays func() ([]string, error)
	// detail runs `mdadm --detail` for one array and returns its output.
	detail func(device string) (string, error)
}

func (s *mdadmSource) Name() string { return "mdadm" }

func (s *mdadmSource) CollectL2() ([]L2Entity, error) {
	if s.listArrays == nil || s.detail == nil {
		return nil, fmt.Errorf("mdadm not configured")
	}
	arrays, err := s.listArrays()
	if err != nil {
		return nil, err
	}
	entities := make([]L2Entity, 0, len(arrays))
	for _, arr := range arrays {
		output, detailErr := s.detail(arr)
		if strings.TrimSpace(output) == "" {
			reason := "no output"
			if detailErr != nil {
				reason = detailErr.Error()
			}
			entities = append(entities, L2Entity{Name: arr, Kind: KindMD, State: L2Unknown, Reason: reason})
			continue
		}
		entities = append(entities, parseMdadmDetail(arr, output))
	}
	return entities, nil
}

// parseMdadmDetail extracts the array state from `mdadm --detail` output.
// The State line may combine flags ("clean, degraded", "active, degraded,
// recovering"); mdState maps the combination to a single normalized state,
// preferring the worst. Failed or faulty member devices from the device
// table are named in the reason when the array is not clean.
func parseMdadmDetail(device, output string) L2Entity {
	entity := L2Entity{Name: device, Kind: KindMD, State: L2Unknown, Reason: "state line not found"}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "State :") {
			state := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "State :")))
			entity.State, entity.Reason = mdState(state)
		}
	}

	if entity.State != L2Clean {
		if failed := mdadmFailedMembers(output); len(failed) > 0 {
			if entity.Reason != "" {
				entity.Reason += "; "
			}
			entity.Reason += "сбойные члены: " + strings.Join(failed, ", ")
		}
	}
	return entity
}

// mdState maps the mdadm State line to a normalized L2 state, preferring
// the worst flag in combined states.
func mdState(state string) (L2State, string) {
	switch {
	case strings.Contains(state, "failed"), strings.Contains(state, "inactive"):
		return L2Failed, state
	case strings.Contains(state, "recovering"), strings.Contains(state, "resync"),
		strings.Contains(state, "rebuild"), strings.Contains(state, "checking"):
		return L2Rebuilding, state
	case strings.Contains(state, "degraded"):
		return L2Degraded, state
	case strings.Contains(state, "clean"), strings.Contains(state, "active"):
		return L2Clean, ""
	default:
		return L2Unknown, state
	}
}

// mdadmFailedMembers extracts member device names of failed/faulty rows
// from the `mdadm --detail` device table.
func mdadmFailedMembers(output string) []string {
	var failed []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "failed") || strings.Contains(line, "faulty") {
			fields := strings.Fields(line)
			if len(fields) >= 7 {
				failed = append(failed, fields[len(fields)-1])
			}
		}
	}
	return failed
}

// parseMdstat extracts array names from /proc/mdstat content: lines like
// "md0 : active raid1 sda1[0] sdb1[1]".
func parseMdstat(content string) []string {
	var arrays []string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "md") && strings.Contains(line, " : ") {
			arrays = append(arrays, strings.Fields(line)[0])
		}
	}
	return arrays
}
