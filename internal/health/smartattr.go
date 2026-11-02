package health

// SMART attribute table parsing for the WMI SMART source
// (MSStorageDriver_FailurePredictData.VendorSpecific). The blob is the
// standard ATA SMART READ DATA buffer: a WORD version followed by up to 30
// attribute records of 12 bytes each:
//
//	offset  size  meaning
//	0       2     version (WORD, little-endian)
//	2+12n   1     attribute id (0 terminates the table)
//	+1      2     status flags
//	+3      1     current normalized value
//	+4      8     raw value, counters little-endian in the first bytes

// SMART attribute ids the watch consumes.
const (
	smartAttrRealloc = 5   // Reallocated_Sector_Ct
	smartAttrPending = 197 // Current_Pending_Sector
)

// parseSMARTAttributes parses a VendorSpecific blob into attribute id ->
// raw value. A short or invalid blob yields an empty table, not an error:
// WMI instances vary across drivers and bridges, and a disk without a
// parseable table is simply a disk without counters.
func parseSMARTAttributes(raw []byte) map[byte]uint64 {
	out := make(map[byte]uint64)
	const (
		versionSize = 2 // WORD, little-endian
		recSize     = 12
	)
	if len(raw) < versionSize+recSize {
		return out
	}
	n := (len(raw) - versionSize) / recSize
	if n > 30 {
		n = 30
	}
	for i := 0; i < n; i++ {
		rec := raw[versionSize+i*recSize : versionSize+(i+1)*recSize]
		id := rec[0]
		if id == 0 {
			break // id 0 terminates the table
		}
		out[id] = smartRawValue(rec[4:12])
	}
	return out
}

// smartRawValue reads the little-endian counter out of an attribute's raw
// field: for the sector counters the watch consumes the real value lives in
// the first bytes of the 8-byte raw area.
func smartRawValue(raw []byte) uint64 {
	var v uint64
	for i := 0; i < len(raw) && i < 8; i++ {
		v |= uint64(raw[i]) << (8 * i)
	}
	return v
}
