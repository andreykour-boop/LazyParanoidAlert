//go:build windows

package health

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// physicalDiskSource collects L1 physical disks on Windows. Primary path is
// COM/WMI (MSFT_PhysicalDisk in the Storage namespace) via go-ole; if COM
// fails on exotic builds, a powershell Get-PhysicalDisk fallback runs.
// Both paths end in the same records; the powershell JSON parser is a pure
// function covered by tests.
type physicalDiskSource struct {
	// query overrides the whole collect (tests); nil = WMI with fallback.
	query func() ([]L1Disk, error)
	// smartctl enriches collected records with counters read through the
	// smartctl binary (primary source: honest raw values, NVMe included);
	// smart enriches from root\wmi (fallback). Both nil disable enrichment
	// (tests). Enrichment never fails the collection: a disk without
	// counters is simply a disk without counters.
	smartctl *smartctlCountersSource
	smart    *smartCountersSource
}

func (s *physicalDiskSource) Name() string { return "physicaldisk" }

func (s *physicalDiskSource) CollectL1() ([]L1Disk, error) {
	if s.query != nil {
		disks, err := s.query()
		if err != nil {
			return nil, err
		}
		s.enrichCounters(disks)
		return disks, nil
	}
	disks, wmiErr := queryPhysicalDisksWMI()
	if wmiErr == nil {
		s.enrichCounters(disks)
		return disks, nil
	}
	disks, psErr := queryPhysicalDisksPowerShell()
	if psErr == nil {
		s.enrichCounters(disks)
		return disks, nil
	}
	return nil, fmt.Errorf("WMI: %v; powershell: %v", wmiErr, psErr)
}

// enrichCounters applies the counter sources in priority order: smartctl
// wins when it matched at least one disk (honest raw semantics, NVMe
// included); the WMI path runs when smartctl is missing or answered
// nothing. CountersSrc is stamped by whichever source delivered, so the
// diff re-baselines across a source switch instead of comparing values
// with different semantics (WMI reports normalized "health" values where
// smartctl reports sector counts).
func (s *physicalDiskSource) enrichCounters(disks []L1Disk) {
	if s.smartctl != nil && s.smartctl.enrich(disks) > 0 {
		return
	}
	s.smart.enrich(disks)
}

// wmiForEach runs a WQL query in the given namespace and calls fn for each
// result object. It initializes COM for the calling thread.
func wmiForEach(namespace, wql string, fn func(item *ole.IDispatch) error) error {
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err == nil {
		// Only the thread that successfully initialized must uninitialize.
		defer ole.CoUninitialize()
	}

	locUnknown, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return fmt.Errorf("create SWbemLocator: %w", err)
	}
	defer locUnknown.Release()
	loc, err := locUnknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("query SWbemLocator: %w", err)
	}
	defer loc.Release()

	svcRaw, err := oleutil.CallMethod(loc, "ConnectServer", nil, namespace)
	if err != nil {
		return fmt.Errorf("ConnectServer(%s): %w", namespace, err)
	}
	svc := svcRaw.ToIDispatch()
	defer svc.Release()

	resultRaw, err := oleutil.CallMethod(svc, "ExecQuery", wql)
	if err != nil {
		return fmt.Errorf("ExecQuery(%q): %w", wql, err)
	}
	result := resultRaw.ToIDispatch()
	defer result.Release()

	enumRaw, err := oleutil.GetProperty(result, "_NewEnum")
	if err != nil {
		return fmt.Errorf("_NewEnum: %w", err)
	}
	enum, err := enumRaw.ToIUnknown().IEnumVARIANT(ole.IID_IEnumVariant)
	if err != nil {
		return fmt.Errorf("IEnumVariant: %w", err)
	}
	defer enum.Release()

	for {
		// Note: go-ole maps IEnumVARIANT's S_FALSE (clean end of
		// enumeration, fetched 0 of 1) to an error, so the fetched
		// count is checked first — same as oleutil.ForEach.
		itemRaw, fetched, err := enum.Next(1)
		if fetched == 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("enum next: %w", err)
		}
		item := itemRaw.ToIDispatch()
		err = fn(item)
		item.Release()
		if err != nil {
			return err
		}
	}
}

const storageNamespace = `ROOT\MICROSOFT\WINDOWS\STORAGE`

// queryPhysicalDisksWMI reads MSFT_PhysicalDisk through COM.
// HealthStatus: 0=Healthy, 1=Warning, 5=Unhealthy.
func queryPhysicalDisksWMI() ([]L1Disk, error) {
	var disks []L1Disk
	err := wmiForEach(storageNamespace,
		"SELECT DeviceId, FriendlyName, Model, SerialNumber, HealthStatus, Size FROM MSFT_PhysicalDisk",
		func(item *ole.IDispatch) error {
			get := func(name string) string {
				v, err := oleutil.GetProperty(item, name)
				if err != nil {
					return ""
				}
				return strings.TrimSpace(fmt.Sprintf("%v", v.Value()))
			}
			health, _ := oleutil.GetProperty(item, "HealthStatus")
			status := StatusUnknown
			switch toInt(health.Value()) {
			case 0:
				status = StatusOK
			case 1:
				status = StatusWarn
			case 5:
				status = StatusFail
			}
			name := get("FriendlyName")
			if name == "" {
				name = get("DeviceId")
			}
			disks = append(disks, L1Disk{
				Name:   name,
				Model:  get("Model"),
				Serial: get("SerialNumber"),
				Status: status,
			})
			return nil
		})
	if err != nil {
		return nil, err
	}
	if disks == nil {
		return nil, fmt.Errorf("WMI returned no physical disks")
	}
	return disks, nil
}

// toInt converts a COM variant value (int, uint16, string...) to int64.
func toInt(v interface{}) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	case string:
		var n int64
		fmt.Sscanf(x, "%d", &n)
		return n
	default:
		return -1
	}
}

// powershellPath resolves powershell.exe: PATH first, then the well-known
// System32 location (PATH is not always inherited into service contexts).
func powershellPath() string {
	if p, err := exec.LookPath("powershell.exe"); err == nil {
		return p
	}
	return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
}

// powershellJSON runs a PowerShell command and returns its JSON output.
func powershellJSON(script string) ([]byte, error) {
	out, err := exec.Command(powershellPath(), "-NoProfile", "-NonInteractive", "-Command",
		script+" | ConvertTo-Json -Compress").Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// queryPhysicalDisksPowerShell is the WMI fallback: Get-PhysicalDisk JSON.
func queryPhysicalDisksPowerShell() ([]L1Disk, error) {
	out, err := powershellJSON(
		"Get-PhysicalDisk | Select-Object DeviceId,FriendlyName,Model,SerialNumber,HealthStatus")
	if err != nil {
		return nil, fmt.Errorf("powershell Get-PhysicalDisk: %w", err)
	}
	return parsePhysicalDiskJSON(out)
}

// psPhysicalDisk is one record of Get-PhysicalDisk JSON output.
type psPhysicalDisk struct {
	DeviceId     string      `json:"DeviceId"`
	FriendlyName string      `json:"FriendlyName"`
	Model        string      `json:"Model"`
	SerialNumber string      `json:"SerialNumber"`
	HealthStatus interface{} `json:"HealthStatus"`
}

// parsePhysicalDiskJSON parses Get-PhysicalDisk | ConvertTo-Json output
// (an object for a single disk, an array for several).
func parsePhysicalDiskJSON(raw []byte) ([]L1Disk, error) {
	var single psPhysicalDisk
	if err := json.Unmarshal(raw, &single); err == nil && single.DeviceId != "" || single.FriendlyName != "" {
		return []L1Disk{psDiskToL1(single)}, nil
	}
	var many []psPhysicalDisk
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("parse Get-PhysicalDisk output: %w", err)
	}
	disks := make([]L1Disk, 0, len(many))
	for _, d := range many {
		disks = append(disks, psDiskToL1(d))
	}
	return disks, nil
}

func psDiskToL1(d psPhysicalDisk) L1Disk {
	name := d.FriendlyName
	if name == "" {
		name = d.DeviceId
	}
	status := StatusUnknown
	switch strings.ToLower(fmt.Sprintf("%v", d.HealthStatus)) {
	case "healthy":
		status = StatusOK
	case "warning":
		status = StatusWarn
	case "unhealthy":
		status = StatusFail
	}
	return L1Disk{Name: name, Model: d.Model, Serial: d.SerialNumber, Status: status}
}

// storagePoolSource collects L2 pools (Storage Spaces and controller pools
// visible to the Storage stack): MSFT_StoragePool, primordial excluded.
type storagePoolSource struct {
	query func() ([]L2Entity, error)
}

func (s *storagePoolSource) Name() string { return "storagepool" }

func (s *storagePoolSource) CollectL2() ([]L2Entity, error) {
	if s.query != nil {
		return s.query()
	}
	pools, wmiErr := queryStoragePoolsWMI()
	if wmiErr == nil {
		return pools, nil
	}
	pools, psErr := queryStoragePoolsPowerShell()
	if psErr == nil {
		return pools, nil
	}
	return nil, fmt.Errorf("WMI: %v; powershell: %v", wmiErr, psErr)
}

// queryStoragePoolsWMI reads MSFT_StoragePool (non-primordial) via COM.
// HealthStatus: 0=Healthy, 1=Warning, 2=Unhealthy.
func queryStoragePoolsWMI() ([]L2Entity, error) {
	var pools []L2Entity
	err := wmiForEach(storageNamespace,
		"SELECT FriendlyName, HealthStatus FROM MSFT_StoragePool WHERE Primordial=false",
		func(item *ole.IDispatch) error {
			get := func(name string) string {
				v, err := oleutil.GetProperty(item, name)
				if err != nil {
					return ""
				}
				return strings.TrimSpace(fmt.Sprintf("%v", v.Value()))
			}
			health, _ := oleutil.GetProperty(item, "HealthStatus")
			state := L2Unknown
			switch toInt(health.Value()) {
			case 0:
				state = L2Clean
			case 1:
				state = L2Degraded
			case 2:
				state = L2Failed
			}
			pools = append(pools, L2Entity{
				Name:  get("FriendlyName"),
				Kind:  KindPool,
				State: state,
			})
			return nil
		})
	if err != nil {
		return nil, err
	}
	return pools, nil
}

// queryStoragePoolsPowerShell is the WMI fallback for pools.
func queryStoragePoolsPowerShell() ([]L2Entity, error) {
	out, err := powershellJSON(
		"Get-StoragePool | Where-Object {$_.IsPrimordial -eq $false} | Select-Object FriendlyName,HealthStatus")
	if err != nil {
		return nil, fmt.Errorf("powershell Get-StoragePool: %w", err)
	}
	return parseStoragePoolJSON(out)
}

// psStoragePool is one record of Get-StoragePool JSON output.
type psStoragePool struct {
	FriendlyName string      `json:"FriendlyName"`
	HealthStatus interface{} `json:"HealthStatus"`
}

// parseStoragePoolJSON parses Get-StoragePool | ConvertTo-Json output.
func parseStoragePoolJSON(raw []byte) ([]L2Entity, error) {
	var single psStoragePool
	if err := json.Unmarshal(raw, &single); err == nil && single.FriendlyName != "" {
		return []L2Entity{psPoolToL2(single)}, nil
	}
	var many []psStoragePool
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("parse Get-StoragePool output: %w", err)
	}
	pools := make([]L2Entity, 0, len(many))
	for _, p := range many {
		pools = append(pools, psPoolToL2(p))
	}
	return pools, nil
}

func psPoolToL2(p psStoragePool) L2Entity {
	state := L2Unknown
	switch strings.ToLower(fmt.Sprintf("%v", p.HealthStatus)) {
	case "healthy":
		state = L2Clean
	case "warning":
		state = L2Degraded
	case "unhealthy":
		state = L2Failed
	}
	return L2Entity{Name: p.FriendlyName, Kind: KindPool, State: state}
}

// NewPlatformSources assembles the platform collectors: physical disks and
// storage pools via WMI/powershell, volumes via drive letters, vendor RAID
// tools when installed (an honest "controller level unverified" error
// source replaces them when absent).
func NewPlatformSources() (l1 []L1Source, l2 []L2Source, vols []VolumesSource) {
	l1 = append(l1, &physicalDiskSource{smartctl: newSmartctlCountersSource(), smart: newSmartCountersSource()})
	if cli := findVendorCLI(filepath.SplitList(os.Getenv("PATH")), nil); cli != nil {
		c := *cli
		l1 = append(l1, &vendorCLISource{cli: c, smartctl: findTool("smartctl"),
			probe: probeWindowsVendorDisk})
	} else {
		l1 = append(l1, &absentVendorCLISource{})
	}
	l2 = append(l2, &storagePoolSource{})
	vols = append(vols, &volumesSourceWin{})
	return l1, l2, vols
}

// probeWindowsVendorDisk queries one disk behind a hardware controller via
// smartctl on Windows (physical drive 0 is the attachment point).
func probeWindowsVendorDisk(diskIndex int, deviceType string) (string, error) {
	bin := findTool("smartctl")
	if bin == "" {
		return "", errSmartctlMissing
	}
	out, err := exec.Command(bin, "-d", deviceType+","+strconv.Itoa(diskIndex),
		"-i", "-H", "-A", `\\.\PhysicalDrive0`).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// SMART sector counters on Windows come from root\wmi:
// MSStorageDriver_FailurePredictData carries the raw ATA attribute table
// per drive. The class is ATA/SATA only (USB bridges and NVMe controllers
// do not expose it) and requires elevation — the service runs as
// LocalSystem and the control UI has a requireAdministrator manifest, so
// both runtimes have access.

// winDrive is one Win32_DiskDrive record used to map serial numbers to
// PNP device id prefixes.
type winDrive struct {
	PNPDeviceID  string
	SerialNumber string
	Model        string
}

// smartInstance is one MSStorageDriver_FailurePredictData record.
type smartInstance struct {
	InstanceName   string
	VendorSpecific []byte
}

// smartCountersSource reads the SMART attribute tables and matches them to
// L1 disk records by serial number. Every query is injectable for tests.
type smartCountersSource struct {
	drives      func() ([]winDrive, error)
	failureData func() ([]smartInstance, error)
}

func newSmartCountersSource() *smartCountersSource {
	return &smartCountersSource{
		drives:      queryWin32DiskDrives,
		failureData: queryFailurePredictData,
	}
}

// enrich fills Realloc/Pending on the matching L1 records. All failures are
// swallowed by design: counters are a bonus, and a disk that stays without
// them must not degrade into an error or a broken collection.
func (s *smartCountersSource) enrich(disks []L1Disk) {
	if s == nil || len(disks) == 0 {
		return
	}
	drv, err := s.drives()
	if err != nil {
		// Fallback path: match SMART instances to disks by normalized
		// model, when Win32_DiskDrive is unavailable.
		s.enrichByModel(disks)
		return
	}
	data, err := s.failureData()
	if err != nil {
		return
	}

	// serial(norm) -> PNPDeviceID, then counters by PNP prefix or tail.
	pnpBySerial := make(map[string]string, len(drv))
	for _, d := range drv {
		if sn := normSerial(d.SerialNumber); sn != "" {
			pnpBySerial[sn] = d.PNPDeviceID
		}
	}
	countersByPrefix := map[string][2]int64{}
	countersByTail := map[string][2]int64{}
	prefixHits := map[string]int{}
	for _, inst := range data {
		attrs := parseSMARTAttributes(inst.VendorSpecific)
		r, hasR := attrs[smartAttrRealloc]
		p, hasP := attrs[smartAttrPending]
		if !hasR && !hasP {
			continue
		}
		c := [2]int64{int64(r), int64(p)}
		// The devnode tail is the precise key (unique per drive); the
		// two-segment prefix is only a fallback and is dropped when
		// ambiguous (identical-model disks share it).
		countersByTail[instTail(inst.InstanceName)] = c
		pre := pnpPrefix(inst.InstanceName)
		countersByPrefix[pre] = c
		prefixHits[pre]++
	}
	for p, n := range prefixHits {
		if n > 1 {
			delete(countersByPrefix, p)
		}
	}
	if len(countersByTail) == 0 {
		return
	}
	for i := range disks {
		pnp, ok := pnpBySerial[normSerial(disks[i].Serial)]
		if !ok {
			continue
		}
		// Two spellings exist for the same drive: PNPDeviceID says
		// "SCSI\DISK&VEN_TOSHIBA&PROD_MD04ACA600\5&3955aa6&0&030000"
		// while the SMART InstanceName says
		// "SCSI\Disk&Ven_TOSHIBA&Prod_MD04ACA600\5&3955aa6&0&030000_0".
		// The devnode tail matches once the "_<digits>" instance index
		// is stripped; the prefix key covers exotic spellings.
		c, ok := countersByTail[instTail(pnp)]
		if !ok {
			c, ok = countersByPrefix[pnpPrefix(pnp)]
		}
		if ok {
			disks[i].Realloc = c[0]
			disks[i].Pending = c[1]
			disks[i].CountersSrc = "wmi"
		}
	}
}

// enrichByModel is the fallback matcher: a normalized disk model compared
// against the normalized SMART InstanceName ("IDE\DiskTOSHIBA_MD04ACA600...").
// Ambiguous models (two identical disks) get the first unmatched instance.
func (s *smartCountersSource) enrichByModel(disks []L1Disk) {
	data, err := s.failureData()
	if err != nil {
		return
	}
	type assignment struct {
		instance smartInstance
		attrs    map[byte]uint64
	}
	var instances []assignment
	for _, inst := range data {
		attrs := parseSMARTAttributes(inst.VendorSpecific)
		if _, ok := attrs[smartAttrRealloc]; !ok {
			if _, ok := attrs[smartAttrPending]; !ok {
				continue
			}
		}
		instances = append(instances, assignment{inst, attrs})
	}
	if len(instances) == 0 {
		return
	}
	used := make([]bool, len(instances))
	for i := range disks {
		model := normID(disks[i].Model)
		if model == "" {
			continue
		}
		for j, a := range instances {
			if used[j] {
				continue
			}
			// The "&Ven_"/"&Prod_" markers would wedge themselves into
			// the model text ("diskventoshibaprodmd04aca600"); split
			// them out before the substring match.
			inst := normID(instModelText(a.instance.InstanceName))
			if strings.Contains(inst, model) {
				disks[i].Realloc = int64(a.attrs[smartAttrRealloc])
				disks[i].Pending = int64(a.attrs[smartAttrPending])
				disks[i].CountersSrc = "wmi"
				used[j] = true
				break
			}
		}
	}
}

// instModelText lowers an instance name and replaces the PNP-style vendor
// markers with separators, so "SCSI\Disk&Ven_TOSHIBA&Prod_MD04ACA600\..."
// and "IDE\DiskTOSHIBA_MD04ACA600\..." both yield a comparable model text.
func instModelText(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&ven_", "\\")
	s = strings.ReplaceAll(s, "&prod_", "\\")
	return s
}

// queryWin32DiskDrives reads Win32_DiskDrive via COM: serial number to
// PNPDeviceID mapping for the SMART instance matching.
func queryWin32DiskDrives() ([]winDrive, error) {
	var drives []winDrive
	err := wmiForEach(`ROOT\CIMV2`,
		"SELECT PNPDeviceID, SerialNumber, Model FROM Win32_DiskDrive",
		func(item *ole.IDispatch) error {
			get := func(name string) string {
				v, err := oleutil.GetProperty(item, name)
				if err != nil {
					return ""
				}
				return strings.TrimSpace(fmt.Sprintf("%v", v.Value()))
			}
			drives = append(drives, winDrive{
				PNPDeviceID:  get("PNPDeviceID"),
				SerialNumber: get("SerialNumber"),
				Model:        get("Model"),
			})
			return nil
		})
	return drives, err
}

// queryFailurePredictData reads MSStorageDriver_FailurePredictData via
// powershell. The direct go-ole IDispatch path delivers the VendorSpecific
// SAFEARRAY corrupted (512 bytes of 0xFF on real hardware, verified by the
// smartprobe diagnostic), while powershell reads the same property
// correctly; base64 keeps the bytes intact through the JSON transport.
func queryFailurePredictData() ([]smartInstance, error) {
	out, err := powershellJSON(
		`Get-CimInstance -Namespace root\wmi -ClassName MSStorageDriver_FailurePredictData | Select-Object InstanceName,@{n="VendorB64";e={[Convert]::ToBase64String($_.VendorSpecific)}}`)
	if err != nil {
		return nil, fmt.Errorf("powershell FailurePredictData: %w", err)
	}
	return parseFailurePredictJSON(out)
}

// psSmartRecord is one FailurePredictData record with the VendorSpecific
// blob base64-encoded for the JSON transport.
type psSmartRecord struct {
	InstanceName string `json:"InstanceName"`
	VendorB64    string `json:"VendorB64"`
}

// parseFailurePredictJSON parses the powershell output (an object for a
// single instance, an array for several).
func parseFailurePredictJSON(raw []byte) ([]smartInstance, error) {
	var many []psSmartRecord
	if err := json.Unmarshal(raw, &many); err == nil {
		return smartRecords(many), nil
	}
	var single psSmartRecord
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil, fmt.Errorf("parse FailurePredictData output: %w", err)
	}
	return smartRecords([]psSmartRecord{single}), nil
}

// smartRecords decodes the base64 blobs; an undecodable record is skipped,
// not fatal — counters are a bonus.
func smartRecords(rs []psSmartRecord) []smartInstance {
	out := make([]smartInstance, 0, len(rs))
	for _, r := range rs {
		blob, err := base64.StdEncoding.DecodeString(r.VendorB64)
		if err != nil {
			continue
		}
		out = append(out, smartInstance{InstanceName: r.InstanceName, VendorSpecific: blob})
	}
	return out
}

// pnpPrefix reduces a PNP device id or SMART instance name to its first two
// "\"-segments ("IDE\DISKTOSHIBA_MD04ACA600..."), the stable part shared by
// both spellings.
func pnpPrefix(id string) string {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(id)), "\\", 3)
	if len(parts) < 2 {
		return strings.ToLower(strings.TrimSpace(id))
	}
	return parts[0] + "\\" + parts[1]
}

// instTail returns the last "\"-segment of a PNP device id or SMART
// instance name ("5&3955aa6&0&030000"): the devnode key that survives the
// VEN_/PROD_ vs Disk spelling difference between the two id formats. The
// "_<digits>" instance index WMI appends ("...030000_0") is stripped.
func instTail(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(s, "\\"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "_"); i >= 0 && isDigits(s[i+1:]) {
		s = s[:i]
	}
	return s
}

// isDigits reports whether s consists of ASCII digits only.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// normSerial normalizes a serial number for matching: lowercase, spaces
// removed (WMI sometimes pads with spaces).
func normSerial(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "")
}

// normID reduces an identifier to lowercase alphanumeric characters, for
// tolerant model matching across "TOSHIBA MD04ACA600" and
// "DiskTOSHIBA_MD04ACA600".
func normID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
