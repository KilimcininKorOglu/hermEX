package nspi

import (
	"encoding/binary"
	"runtime"
	"testing"

	"hermex/internal/ext"
)

// TestAForgedStringCountIsRefusedBeforeAllocating hands each string-array reader
// a count of 2^26 names followed by no names at all. Every name costs at least
// one byte on the wire, so the count cannot be satisfied; a reader that sized
// its slice from the count first would reserve a gigabyte for a 4-byte request,
// and a count near 2^32 would take the address book daemon down.
func TestAForgedStringCountIsRefusedBeforeAllocating(t *testing.T) {
	body := binary.LittleEndian.AppendUint32(nil, 1<<26)
	readers := map[string]func(*ext.Pull) ([]string, error){
		"STRING_ARRAY":  pullStringArray,
		"WSTRING_ARRAY": pullWStringArray,
	}
	for name, read := range readers {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		got, err := read(ext.NewPull(body, abkFlags))
		runtime.ReadMemStats(&after)
		if err == nil {
			t.Errorf("%s: a count the buffer cannot hold was accepted", name)
		}
		if got != nil {
			t.Errorf("%s: the refused read returned %d strings, want none", name, len(got))
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
			t.Errorf("%s: reading the forged count allocated %d bytes", name, grew)
		}
	}
}
