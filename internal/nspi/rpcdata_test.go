package nspi

import (
	"encoding/binary"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// inHandle starts an NDR IN stub with the 20-byte context handle every data op
// carries (the server discards it).
func inHandle() *ndr.Push {
	p := ndr.NewPush()
	pushCtxHandleNDR(p, 0, mapi.GUID{})
	return p
}

// tailResult reads the trailing result u32 — the last field of every NSPI OUT,
// regardless of the variable-length body before it.
func tailResult(out []byte) uint32 {
	return binary.LittleEndian.Uint32(out[len(out)-4:])
}

// pushStringsArrayForTest writes a StringsArray_r / WStringsArray_r IN: count +
// referent + max_count(==count) + per-element referents (all present) + each
// element's conformant-varying NUL-terminated string. The server never emits one,
// so the framing is written explicitly here.
func pushStringsArrayForTest(p *ndr.Push, names []string, wide bool) {
	n := uint32(len(names))
	p.Uint32(n)
	p.UniquePtr(true) // array referent
	p.Uint32(n)       // max_count == count
	for range names {
		p.UniquePtr(true) // per-element referent
	}
	for _, name := range names {
		if wide {
			b := encodeUTF16LE(name) // includes the 2-byte terminator
			units := uint32(len(b) / 2)
			p.Uint32(units) // max_count
			p.Uint32(0)     // offset
			p.Uint32(units) // actual
			p.Raw(b)
		} else {
			b := append([]byte(name), 0) // NUL-terminated
			ln := uint32(len(b))
			p.Uint32(ln)
			p.Uint32(0)
			p.Uint32(ln)
			p.Raw(b)
		}
	}
}

// TestRPCUpdateStat repositions the cursor by STAT.delta and reports the applied
// delta through the [in,out] pointer.
func TestRPCUpdateStat(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	p := inHandle()
	p.Uint32(0) // reserved
	pushStatNDR(p, stat{curRec: midBase, delta: 2, codePage: 1252})
	p.UniquePtr(true) // delta requested
	p.Int32(0)        // in delta value, ignored
	out, fault := s.DispatchRPC(opNspiUpdateStat, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	st, err := pullStatNDR(q)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	ref, _ := q.Uint32()
	if ref == 0 {
		t.Fatal("delta referent absent, want present")
	}
	delta, _ := q.Int32()
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
	if delta != 2 {
		t.Errorf("applied delta = %d, want 2", delta)
	}
	if st.curRec != midBase+2 {
		t.Errorf("curRec = %#x, want %#x", st.curRec, midBase+2)
	}
}

// TestRPCQueryRowsExplicit returns one row per explicit MId.
func TestRPCQueryRowsExplicit(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	p := inHandle()
	p.Uint32(0) // flags
	pushStatNDR(p, stat{codePage: 1252})
	p.Uint32(2)           // inline MID count
	p.UniquePtr(true)     // referent
	p.Uint32(2)           // max_count == count
	p.Uint32(midBase)     // alice
	p.Uint32(midBase + 2) // carol
	p.Uint32(10)          // requested
	p.UniquePtr(false)    // default columns
	out, fault := s.DispatchRPC(opNspiQueryRows, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	if _, err := pullStatNDR(q); err != nil {
		t.Fatalf("stat: %v", err)
	}
	ref, _ := q.Uint32()
	if ref == 0 {
		t.Fatal("rowset referent absent on success")
	}
	crows, _ := q.Uint32()
	if crows != 2 {
		t.Errorf("rowset crows = %d, want 2", crows)
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
}

// TestRPCQueryRowsInvalid rejects a zero row count with a null rowset referent.
func TestRPCQueryRowsInvalid(t *testing.T) {
	s := testGAL("alice@hermex.test")
	p := inHandle()
	p.Uint32(0)
	pushStatNDR(p, stat{codePage: 1252})
	p.Uint32(0)        // inline MID count: none
	p.UniquePtr(false) // null MID referent
	p.Uint32(0)        // requested count == 0 → ecInvalidParam
	p.UniquePtr(false) // default columns
	out, fault := s.DispatchRPC(opNspiQueryRows, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	if _, err := pullStatNDR(q); err != nil {
		t.Fatalf("stat: %v", err)
	}
	ref, _ := q.Uint32()
	if ref != 0 {
		t.Error("rowset referent present on failure, want null")
	}
	if tailResult(out) != ecInvalidParam {
		t.Errorf("result %#x, want ecInvalidParam", tailResult(out))
	}
}

// TestRPCSeekEntries positions at the first entry at or after the target name.
func TestRPCSeekEntries(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	p := inHandle()
	p.Uint32(0) // reserved
	pushStatNDR(p, stat{sortType: sortTypeDisplayName, codePage: 1252})
	if err := pushPropValHeaderNDR(p, mapi.PrDisplayName, "bob@hermex.test"); err != nil {
		t.Fatalf("target header: %v", err)
	}
	if err := pushPropValContentNDR(p, mapi.PrDisplayName, "bob@hermex.test"); err != nil {
		t.Fatalf("target content: %v", err)
	}
	p.UniquePtr(false) // no MID table
	p.UniquePtr(false) // default columns
	out, fault := s.DispatchRPC(opNspiSeekEntries, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	st, err := pullStatNDR(q)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	ref, _ := q.Uint32()
	if ref == 0 {
		t.Fatal("rowset referent absent on success")
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
	if st.curRec != midBase+1 { // bob is the second entry
		t.Errorf("curRec = %#x, want bob %#x", st.curRec, midBase+1)
	}
}

// TestRPCCompareMids returns the signed table order of two MIds before the result.
func TestRPCCompareMids(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	p := inHandle()
	p.Uint32(0) // reserved
	pushStatNDR(p, stat{codePage: 1252})
	p.Uint32(midBase)     // alice (pos 0)
	p.Uint32(midBase + 2) // carol (pos 2)
	out, fault := s.DispatchRPC(opNspiCompareMIds, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	cmp := int32(binary.LittleEndian.Uint32(out[0:4]))
	if cmp != 1 {
		t.Errorf("cmp(alice, carol) = %d, want 1 (carol sorts later)", cmp)
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
}

// TestRPCResortRestriction sorts the input MIds into display-name (ascending MId)
// order and returns them.
func TestRPCResortRestriction(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	p := inHandle()
	p.Uint32(0) // reserved
	pushStatNDR(p, stat{codePage: 1252})
	pushU32ArrayNDR(p, []uint32{midBase + 2, midBase}) // inline [carol, alice]
	p.UniquePtr(false)                                 // reserved outmids: null
	out, fault := s.DispatchRPC(opNspiResortRestriction, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	if _, err := pullStatNDR(q); err != nil {
		t.Fatalf("stat: %v", err)
	}
	mids, err := pullPtrMIDs(q)
	if err != nil {
		t.Fatalf("mids: %v", err)
	}
	if len(mids) != 2 || mids[0] != midBase || mids[1] != midBase+2 {
		t.Errorf("resorted mids = %v, want [%#x %#x]", mids, midBase, midBase+2)
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
}

// TestRPCGetProps returns the single addressed entry's row.
func TestRPCGetProps(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	p := inHandle()
	p.Uint32(0) // flags
	pushStatNDR(p, stat{curRec: midBase, codePage: 1252})
	pushPtrProptagsForTest(p, []mapi.PropTag{mapi.PrDisplayName, mapi.PrSmtpAddress})
	out, fault := s.DispatchRPC(opNspiGetProps, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	ref, _ := q.Uint32()
	if ref == 0 {
		t.Fatal("row referent absent on success")
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
}

// TestRPCGetPropList lists an entry's available tags, and rejects MId 0.
func TestRPCGetPropList(t *testing.T) {
	s := testGAL("alice@hermex.test")
	p := inHandle()
	p.Uint32(0)       // flags
	p.Uint32(midBase) // alice
	p.Uint32(1252)    // code page
	out, fault := s.DispatchRPC(opNspiGetPropList, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	tags, err := pullPtrProptags(ndr.NewPull(out))
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(tags) != len(defaultColumns) {
		t.Errorf("tags = %d, want %d", len(tags), len(defaultColumns))
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}

	// MId 0 is an invalid object: null proptag referent.
	p2 := inHandle()
	p2.Uint32(0)
	p2.Uint32(0) // MId 0
	p2.Uint32(1252)
	out2, _ := s.DispatchRPC(opNspiGetPropList, p2.Bytes())
	if ref, _ := ndr.NewPull(out2).Uint32(); ref != 0 {
		t.Error("proptag referent present for MId 0, want null")
	}
	if tailResult(out2) != ecInvalidObject {
		t.Errorf("MId 0 result %#x, want ecInvalidObject", tailResult(out2))
	}
}

// TestRPCQueryColumns returns the fixed address-book column set.
func TestRPCQueryColumns(t *testing.T) {
	s := testGAL("alice@hermex.test")
	p := inHandle()
	p.Uint32(0) // reserved
	p.Uint32(0) // flags
	out, fault := s.DispatchRPC(opNspiQueryColumns, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	tags, err := pullPtrProptags(ndr.NewPull(out))
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(tags) != len(defaultColumns) {
		t.Errorf("columns = %d, want %d", len(tags), len(defaultColumns))
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
}

// TestRPCGetSpecialTable returns the table version first, then the container row
// set.
func TestRPCGetSpecialTable(t *testing.T) {
	s := testGAL("alice@hermex.test")
	p := inHandle()
	p.Uint32(0) // flags
	pushStatNDR(p, stat{codePage: 1252})
	p.Uint32(0) // client's cached version
	out, fault := s.DispatchRPC(opNspiGetSpecialTable, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	version, _ := q.Uint32()
	if version != galTableVersion {
		t.Errorf("version = %d, want %d", version, galTableVersion)
	}
	ref, _ := q.Uint32()
	if ref == 0 {
		t.Fatal("rowset referent absent on success")
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
}

// --- write-range opnums: each must answer with a MAPI error, never fault. The
// load-bearing assertion in every case is fault==0 AND the expected result: a
// result-only check would still pass while the op silently faulted. ---

// TestRPCModPropsNotSupported: the GAL is read-only, so ModProps answers
// ecNotSupported (the bare result, no echoed row) instead of an op-range fault.
func TestRPCModPropsNotSupported(t *testing.T) {
	s := testGAL("alice@hermex.test")
	out, fault := s.DispatchRPC(opNspiModProps, inHandle().Bytes())
	if fault != 0 {
		t.Fatalf("ModProps faulted %#x; the write-range opnums must answer, not fault", fault)
	}
	if len(out) != 4 {
		t.Fatalf("OUT = %d bytes, want 4 (bare result)", len(out))
	}
	if tailResult(out) != ecNotSupported {
		t.Errorf("result %#x, want ecNotSupported", tailResult(out))
	}
}

// TestRPCModLinkAttNotSupported: hermEX has no delegate/DL-membership editing, so
// ModLinkAtt answers a blanket ecNotSupported.
func TestRPCModLinkAttNotSupported(t *testing.T) {
	s := testGAL("alice@hermex.test")
	out, fault := s.DispatchRPC(opNspiModLinkAtt, inHandle().Bytes())
	if fault != 0 {
		t.Fatalf("ModLinkAtt faulted %#x; the write-range opnums must answer, not fault", fault)
	}
	if len(out) != 4 {
		t.Fatalf("OUT = %d bytes, want 4 (bare result)", len(out))
	}
	if tailResult(out) != ecNotSupported {
		t.Errorf("result %#x, want ecNotSupported", tailResult(out))
	}
}

// TestRPCGetTemplateInfoNoTemplate: a well-formed TI_TEMPLATE request finds no
// display-table archive, so the OUT is a null pData pointer then ecUnknownLcid.
func TestRPCGetTemplateInfoNoTemplate(t *testing.T) {
	s := testGAL("alice@hermex.test")
	p := inHandle()
	p.Uint32(tiTemplate) // Flags
	out, fault := s.DispatchRPC(opNspiGetTemplateInfo, p.Bytes())
	if fault != 0 {
		t.Fatalf("GetTemplateInfo faulted %#x; it must answer, not fault", fault)
	}
	if ref, _ := ndr.NewPull(out).Uint32(); ref != 0 {
		t.Errorf("pData referent = %#x, want 0 (no template row)", ref)
	}
	if tailResult(out) != ecUnknownLcid {
		t.Errorf("result %#x, want ecUnknownLcid", tailResult(out))
	}
}

// TestRPCGetTemplateInfoScriptNotSupported: a request that is not TI_TEMPLATE alone
// (here it also carries TI_SCRIPT) is rejected with ecNotSupported, mirroring the
// reference flags rung — the branch the no-template case does not exercise.
func TestRPCGetTemplateInfoScriptNotSupported(t *testing.T) {
	s := testGAL("alice@hermex.test")
	p := inHandle()
	p.Uint32(tiTemplate | tiScript) // Flags carry TI_SCRIPT
	out, fault := s.DispatchRPC(opNspiGetTemplateInfo, p.Bytes())
	if fault != 0 {
		t.Fatalf("GetTemplateInfo faulted %#x", fault)
	}
	if ref, _ := ndr.NewPull(out).Uint32(); ref != 0 {
		t.Errorf("pData referent = %#x, want 0", ref)
	}
	if tailResult(out) != ecNotSupported {
		t.Errorf("result %#x, want ecNotSupported", tailResult(out))
	}
}

// TestRPCDNToMid maps a distinguished name back to its MId.
func TestRPCDNToMid(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	p := inHandle()
	p.Uint32(0) // reserved
	pushStringsArrayForTest(p, []string{userDN("bob@hermex.test"), "cn=nobody"}, false)
	out, fault := s.DispatchRPC(opNspiDNToMId, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	mids, err := pullPtrMIDs(ndr.NewPull(out))
	if err != nil {
		t.Fatalf("mids: %v", err)
	}
	if len(mids) != 2 || mids[0] != midBase+1 || mids[1] != midUnresolved {
		t.Errorf("mids = %v, want [bob %#x, unresolved %#x]", mids, midBase+1, midUnresolved)
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
}

// TestRPCGetMatches returns the MIds satisfying a PR_ANR restriction.
func TestRPCGetMatches(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	p := inHandle()
	p.Uint32(0) // reserved1
	pushStatNDR(p, stat{sortType: sortTypeDisplayName, codePage: 1252})
	p.UniquePtr(false) // reserved inmids: null
	p.Uint32(0)        // reserved
	p.UniquePtr(true)  // filter present
	pushPropResForTest(t, p, mapi.RelopEQ, mapi.PrAnr, "alice")
	p.UniquePtr(false) // no property name
	p.Uint32(10)       // requested
	p.UniquePtr(false) // default columns
	out, fault := s.DispatchRPC(opNspiGetMatches, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	if _, err := pullStatNDR(q); err != nil {
		t.Fatalf("stat: %v", err)
	}
	mids, err := pullPtrMIDs(q)
	if err != nil {
		t.Fatalf("mids: %v", err)
	}
	if len(mids) != 1 || mids[0] != midBase {
		t.Errorf("matched mids = %v, want [alice %#x]", mids, midBase)
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
}

// TestRPCGetMatchesPropName rejects a present property name as unsupported, with
// the two NULL referents the error path requires.
func TestRPCGetMatchesPropName(t *testing.T) {
	s := testGAL("alice@hermex.test")
	p := inHandle()
	p.Uint32(0) // reserved1
	pushStatNDR(p, stat{sortType: sortTypeDisplayName, codePage: 1252})
	p.UniquePtr(false) // reserved inmids
	p.Uint32(0)        // reserved
	p.UniquePtr(false) // no filter
	p.UniquePtr(true)  // property name present → unsupported
	out, fault := s.DispatchRPC(opNspiGetMatches, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	if _, err := pullStatNDR(q); err != nil {
		t.Fatalf("stat: %v", err)
	}
	midRef, _ := q.Uint32()
	rowRef, _ := q.Uint32()
	if midRef != 0 || rowRef != 0 {
		t.Errorf("error path referents = (%#x, %#x), want both null", midRef, rowRef)
	}
	if tailResult(out) != ecNotSupported {
		t.Errorf("result %#x, want ecNotSupported", tailResult(out))
	}
}

// TestRPCResolveNames resolves an 8-bit name to a per-name status and a row.
func TestRPCResolveNames(t *testing.T) {
	assertResolve(t, opNspiResolveNames, false)
}

// TestRPCResolveNamesW resolves a UTF-16 name identically.
func TestRPCResolveNamesW(t *testing.T) {
	assertResolve(t, opNspiResolveNamesW, true)
}

func assertResolve(t *testing.T, opnum uint16, wide bool) {
	t.Helper()
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	p := inHandle()
	p.Uint32(0) // reserved
	pushStatNDR(p, stat{codePage: 1252})
	p.UniquePtr(false) // default columns
	pushStringsArrayForTest(p, []string{"alice"}, wide)
	out, fault := s.DispatchRPC(opnum, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	mids, err := pullPtrMIDs(q)
	if err != nil {
		t.Fatalf("mids: %v", err)
	}
	if len(mids) != 1 || mids[0] != midResolved {
		t.Errorf("resolve status = %v, want [midResolved %#x]", mids, midResolved)
	}
	ref, _ := q.Uint32() // rowset referent
	if ref == 0 {
		t.Error("rowset referent absent on a resolved name")
	}
	if tailResult(out) != ecSuccess {
		t.Errorf("result %#x, want ecSuccess", tailResult(out))
	}
}

// pushPtrProptagsForTest writes a present unique-pointer proptag array IN.
func pushPtrProptagsForTest(p *ndr.Push, tags []mapi.PropTag) {
	p.UniquePtr(true)
	pushProptagsNDR(p, tags)
}
