package health

import (
	"errors"
	"strings"
	"testing"
)

// Fixture: healthy mirror.
const mdadmDetailClean = `/dev/md0:
           Version : 1.2
     Creation Time : Mon Oct  5 10:00:00 2026
        Raid Level : raid1
        Array Size : 1953382400 (1863.02 GiB 2000.26 GB)
     Used Dev Size : 1953382400 (1863.02 GiB 2000.26 GB)
      Raid Devices : 2
     Total Devices : 2
       Persistence : Superblock is persistent
     Intent Bitmap : Internal
       State : clean

       Name : server:0  (local to host server)
       UUID : 1a2b3c4d:5e6f7081:92a3b4c5:d6e7f809
     Events : 12345

    Number   Major   Minor   RaidDevice State
       0       8        1        0      active sync   /dev/sda1
       1       8       17        1      active sync   /dev/sdb1
`

// Fixture: degraded mirror with a failed member.
const mdadmDetailDegraded = `/dev/md0:
           Version : 1.2
        Raid Level : raid1
      Raid Devices : 2
     Total Devices : 1
       State : clean, degraded
    Number   Major   Minor   RaidDevice State
       0       8        1        0      active sync   /dev/sda1
       1       0        0        1      removed
       -       8       33        -      faulty spare   /dev/sdc1
`

// Fixture: rebuilding after hot-swap.
const mdadmDetailRebuilding = `/dev/md0:
        Raid Level : raid1
       State : clean, degraded, recovering
     Rebuild Status : 45% complete
    Number   Major   Minor   RaidDevice State
       0       8        1        0      active sync   /dev/sda1
       2       8       17        1      spare rebuilding   /dev/sdb1
`

// Fixture: failed array.
const mdadmDetailFailed = `/dev/md0:
        Raid Level : raid1
       State : failed
    Number   Major   Minor   RaidDevice State
       0       0        0        0      removed
       1       0        0        1      removed
`

func TestParseMdadmDetailClean(t *testing.T) {
	entity := parseMdadmDetail("md0", mdadmDetailClean)
	if entity.State != L2Clean {
		t.Errorf("State = %q, want clean", entity.State)
	}
	if entity.Reason != "" {
		t.Errorf("Reason = %q, want empty for clean", entity.Reason)
	}
	if entity.Kind != KindMD {
		t.Errorf("Kind = %q, want md", entity.Kind)
	}
}

func TestParseMdadmDetailDegraded(t *testing.T) {
	entity := parseMdadmDetail("md0", mdadmDetailDegraded)
	if entity.State != L2Degraded {
		t.Errorf("State = %q, want degraded", entity.State)
	}
	if !strings.Contains(entity.Reason, "degraded") {
		t.Errorf("Reason = %q, want the mdadm state inside", entity.Reason)
	}
	if !strings.Contains(entity.Reason, "/dev/sdc1") {
		t.Errorf("Reason = %q, want the faulty member named", entity.Reason)
	}
}

func TestParseMdadmDetailRebuilding(t *testing.T) {
	entity := parseMdadmDetail("md0", mdadmDetailRebuilding)
	if entity.State != L2Rebuilding {
		t.Errorf("State = %q, want rebuilding", entity.State)
	}
	if !strings.Contains(entity.Reason, "recovering") {
		t.Errorf("Reason = %q, want the mdadm state inside", entity.Reason)
	}
}

func TestParseMdadmDetailFailed(t *testing.T) {
	entity := parseMdadmDetail("md0", mdadmDetailFailed)
	if entity.State != L2Failed {
		t.Errorf("State = %q, want failed", entity.State)
	}
}

func TestParseMdadmDetailNoStateLine(t *testing.T) {
	entity := parseMdadmDetail("md0", "some unexpected output")
	if entity.State != L2Unknown || entity.Reason == "" {
		t.Errorf("unexpected output must be unknown with reason: %+v", entity)
	}
}

func TestParseMdstat(t *testing.T) {
	content := `Personalities : [raid1] [linear] [multipath] [raid0] [raid6] [raid5] [raid10] [raid4]
md0 : active raid1 sda1[0] sdb1[1]
      1953382400 blocks super 1.2 [2/2] [UU]

md127 : active raid5 sdd1[0] sde1[1] sdf1[3] sdg1[2]
      5860147200 blocks super 1.2 level 5, 512k chunk, algorithm 2 [4/4] [UUUU]
unused devices: <none>
`
	arrays := parseMdstat(content)
	want := []string{"md0", "md127"}
	if strings.Join(arrays, ",") != strings.Join(want, ",") {
		t.Errorf("arrays = %v, want %v", arrays, want)
	}
}

// TestMdadmSourceCollect: entity per array, per-array failure marked
// unknown, list error propagates as a source error.
func TestMdadmSourceCollect(t *testing.T) {
	s := &mdadmSource{
		listArrays: func() ([]string, error) { return []string{"md0", "md1"}, nil },
		detail: func(device string) (string, error) {
			switch device {
			case "md0":
				return mdadmDetailClean, nil
			case "md1":
				return "", errors.New("mdadm: cannot open /dev/md1")
			}
			return "", errors.New("unexpected array")
		},
	}

	entities, err := s.CollectL2()
	if err != nil {
		t.Fatalf("CollectL2: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("entities = %+v, want 2", entities)
	}
	if entities[0].State != L2Clean {
		t.Errorf("md0 = %+v, want clean", entities[0])
	}
	if entities[1].State != L2Unknown || entities[1].Reason == "" {
		t.Errorf("md1 must be unknown with reason: %+v", entities[1])
	}
}

func TestMdadmSourceListError(t *testing.T) {
	s := &mdadmSource{
		listArrays: func() ([]string, error) { return nil, errors.New("/proc/mdstat unreadable") },
		detail:     func(string) (string, error) { return "", nil },
	}
	if _, err := s.CollectL2(); err == nil {
		t.Fatal("expected error when /proc/mdstat is unreadable")
	}
}
