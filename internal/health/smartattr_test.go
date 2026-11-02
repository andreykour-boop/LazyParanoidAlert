package health

import "testing"

// buildSMARTBlob assembles a VendorSpecific blob in the ATA SMART READ
// DATA layout: WORD version, then 12-byte records (id, flags 2, value 1,
// raw 8).
func buildSMARTBlob(attrs map[byte]uint64) []byte {
	blob := []byte{0x10, 0x00} // version WORD
	for id, raw := range attrs {
		rec := make([]byte, 12)
		rec[0] = id
		for i := 0; i < 8 && i < len(rec[4:]); i++ {
			rec[4+i] = byte(raw >> (8 * i))
		}
		blob = append(blob, rec...)
	}
	return blob
}

// TestParseSMARTAttributes: sector counters are read from their raw fields,
// unrelated attributes are available too, and the table terminator (id 0)
// stops the walk.
func TestParseSMARTAttributes(t *testing.T) {
	blob := buildSMARTBlob(map[byte]uint64{
		5:   481, // Reallocated_Sector_Ct
		194: 32,  // Temperature — raw is a single byte
		197: 0,   // Current_Pending_Sector
		199: 0,   // UDMA_CRC_Error_Count
	})
	attrs := parseSMARTAttributes(blob)
	if got := attrs[smartAttrRealloc]; got != 481 {
		t.Errorf("realloc = %d, want 481", got)
	}
	if got := attrs[smartAttrPending]; got != 0 {
		t.Errorf("pending = %d, want 0", got)
	}
	if got := attrs[194]; got != 32 {
		t.Errorf("temperature = %d, want 32", got)
	}
}

// TestParseSMARTAttributesTerminator: an id-0 record ends the table even if
// more bytes follow.
func TestParseSMARTAttributesTerminator(t *testing.T) {
	blob := buildSMARTBlob(map[byte]uint64{5: 100})
	blob = append(blob, make([]byte, 12)...) // zero record terminates
	blob = append(blob, buildSMARTBlob(map[byte]uint64{5: 999})[1:]...)
	attrs := parseSMARTAttributes(blob)
	if got := attrs[smartAttrRealloc]; got != 100 {
		t.Errorf("realloc = %d, want 100 (records after id 0 must be ignored)", got)
	}
}

// TestParseSMARTAttributesInvalid: short and empty blobs yield an empty
// table, never a panic or an error.
func TestParseSMARTAttributesInvalid(t *testing.T) {
	for name, blob := range map[string][]byte{
		"empty":       {},
		"version":     {0x10, 0x00},
		"no full rec": append([]byte{0x10, 0x00}, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11),
	} {
		if attrs := parseSMARTAttributes(blob); len(attrs) != 0 {
			t.Errorf("%s: want empty table, got %v", name, attrs)
		}
	}
}
