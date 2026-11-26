package objectstore

import (
	"testing"
)

func configVal(t *testing.T, s *Store, id int) uint64 {
	t.Helper()
	var v uint64
	if err := s.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, id).Scan(&v); err != nil {
		t.Fatalf("config %d: %v", id, err)
	}
	return v
}

func TestSeedStore(t *testing.T) {
	s := openTestStore(t)

	if got := configVal(t, s, cfgCurrentEID); got != customEIDBegin {
		t.Errorf("CURRENT_EID = %#x, want %#x", got, customEIDBegin)
	}
	if got := configVal(t, s, cfgMaximumEID); got != 0xFFFF {
		t.Errorf("MAXIMUM_EID = %#x, want 0xFFFF", got)
	}
	if got := configVal(t, s, cfgLastChangeNumber); got != 0 {
		t.Errorf("LAST_CHANGE_NUMBER = %d, want 0", got)
	}
	if g, err := s.storeGUID(); err != nil || len(g) != 36 {
		t.Errorf("mailbox GUID = %q (err=%v), want a 36-char GUID", g, err)
	}
	// The initial allocated range is [1, 0xFFFF].
	var begin, end uint64
	if err := s.objdb.QueryRow(`SELECT range_begin, range_end FROM allocated_eids`).Scan(&begin, &end); err != nil {
		t.Fatal(err)
	}
	if begin != 1 || end != 0xFFFF {
		t.Errorf("initial range = [%#x, %#x], want [1, 0xFFFF]", begin, end)
	}
}

func TestAllocateCNMonotonic(t *testing.T) {
	s := openTestStore(t)
	var prev uint64
	for i := 1; i <= 5; i++ {
		cn, err := allocateCN(s.objdb)
		if err != nil {
			t.Fatal(err)
		}
		if cn != uint64(i) {
			t.Errorf("CN #%d = %d, want %d", i, cn, i)
		}
		if cn <= prev {
			t.Errorf("CN not monotonic: %d after %d", cn, prev)
		}
		prev = cn
	}
	// Persisted across reopen of the counter.
	if got := configVal(t, s, cfgLastChangeNumber); got != 5 {
		t.Errorf("LAST_CHANGE_NUMBER = %d, want 5", got)
	}
}

func TestAllocateEIDMonotonicAndRangeRoll(t *testing.T) {
	s := openTestStore(t)
	// Fresh store hands out customEIDBegin first, monotonically.
	for i, want := range []uint64{0x100, 0x101, 0x102} {
		got, err := allocateEID(s.objdb)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("alloc #%d = %#x, want %#x", i, got, want)
		}
	}

	// Force the boundary: set the cursor to the maximum, then allocate. The
	// allocator must carve a new range and never reuse an id.
	if _, err := s.objdb.Exec(`UPDATE configurations SET config_value=? WHERE config_id=?`, 0xFFFF, cfgCurrentEID); err != nil {
		t.Fatal(err)
	}
	got, err := allocateEID(s.objdb)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x10000 {
		t.Errorf("post-boundary alloc = %#x, want 0x10000", got)
	}
	// A new range was recorded and the maximum advanced.
	if got := configVal(t, s, cfgMaximumEID); got != 0x1FFFF {
		t.Errorf("MAXIMUM_EID after roll = %#x, want 0x1FFFF", got)
	}
	var n int
	if err := s.objdb.QueryRow(`SELECT COUNT(*) FROM allocated_eids`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("allocated_eids rows = %d, want 2 after one roll", n)
	}
}

func TestAllocateRange(t *testing.T) {
	s := openTestStore(t)
	begin, end, err := allocateRange(s.objdb)
	if err != nil {
		t.Fatal(err)
	}
	// First folder range sits immediately above the seed range [1, 0xFFFF].
	if begin != 0x10000 || end != 0x1FFFF {
		t.Errorf("range = [%#x, %#x], want [0x10000, 0x1FFFF]", begin, end)
	}
	b2, _, err := allocateRange(s.objdb)
	if err != nil {
		t.Fatal(err)
	}
	if b2 != 0x20000 {
		t.Errorf("second range begin = %#x, want 0x20000", b2)
	}
}

// TestAllocateLocalIDsRefusesAZeroCount: an IdCount of zero reserves no block
// ([MS-OXCFXICS] 2.2.3.2.1.3 asks for at least one id), so the request is refused
// and moves neither the store cursor nor its maximum.
func TestAllocateLocalIDsRefusesAZeroCount(t *testing.T) {
	s := openTestStore(t)
	cur, maxEID := configVal(t, s, cfgCurrentEID), configVal(t, s, cfgMaximumEID)
	if _, _, err := s.AllocateLocalIDs(0); err == nil {
		t.Error("a zero-count id reservation was granted")
	}
	wantEq(t, "CURRENT_EID after the refused reservation", configVal(t, s, cfgCurrentEID), cur)
	wantEq(t, "MAXIMUM_EID after the refused reservation", configVal(t, s, cfgMaximumEID), maxEID)
}

// TestAllocateLocalIDsAcrossARangeBoundary: RopGetLocalReplicaIds reserves IdCount
// contiguous ids ([MS-OXCFXICS] 2.2.3.2.1.3). A block that does not fit in what
// remains of the current range must still be granted, from ids no other
// allocation has handed out or will hand out, and repeating the request must not
// fail at the same boundary. A block wider than one range is granted too.
func TestAllocateLocalIDsAcrossARangeBoundary(t *testing.T) {
	s := openTestStore(t)
	maxEID := configVal(t, s, cfgMaximumEID)
	if _, err := s.objdb.Exec(`UPDATE configurations SET config_value=? WHERE config_id=?`, maxEID-0x10, cfgCurrentEID); err != nil {
		t.Fatal(err)
	}

	const count = 0x20
	begin, _, err := s.AllocateLocalIDs(count)
	if err != nil {
		t.Fatalf("a block straddling the range end was refused: %v", err)
	}
	if begin < maxEID {
		t.Errorf("block begins at %#x, inside the old range that ends at %#x", begin, maxEID)
	}
	next, err := allocateEID(s.objdb)
	if err != nil {
		t.Fatal(err)
	}
	if next < begin+count {
		t.Errorf("the next server id %#x falls inside the client's block [%#x, %#x)", next, begin, begin+count)
	}

	wide := uint32(3 * 0x10000)
	wbegin, _, err := s.AllocateLocalIDs(wide)
	if err != nil {
		t.Fatalf("a block wider than one range was refused: %v", err)
	}
	after, err := allocateEID(s.objdb)
	if err != nil {
		t.Fatal(err)
	}
	if wbegin <= next || after < wbegin+uint64(wide) {
		t.Errorf("wide block [%#x, %#x) overlaps the ids around it (%#x before, %#x after)", wbegin, wbegin+uint64(wide), next, after)
	}
}
