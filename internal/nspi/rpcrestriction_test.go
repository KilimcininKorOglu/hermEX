package nspi

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// pushPropResForTest emits a RES_PROPERTY restriction in NDR (res_type twice,
// relop, proptag, a value referent, then the PROPERTY_VALUE). The framing is
// written explicitly per the reference layout, not via the puller, so a pull
// against it is not self-confirming.
func pushPropResForTest(t *testing.T, p *ndr.Push, relop mapi.Relop, tag mapi.PropTag, val any) {
	t.Helper()
	p.Uint32(uint32(mapi.ResProperty))
	p.Uint32(uint32(mapi.ResProperty))
	p.Uint32(uint32(relop))
	p.Uint32(uint32(tag))
	p.UniquePtr(true)
	if err := pushPropValHeaderNDR(p, tag, val); err != nil {
		t.Fatalf("push value header: %v", err)
	}
	if err := pushPropValContentNDR(p, tag, val); err != nil {
		t.Fatalf("push value content: %v", err)
	}
}

// pushExistResForTest emits a RES_EXIST restriction in NDR.
func pushExistResForTest(p *ndr.Push, tag mapi.PropTag) {
	p.Uint32(uint32(mapi.ResExist))
	p.Uint32(uint32(mapi.ResExist))
	p.Uint32(0) // reserved1
	p.Uint32(uint32(tag))
	p.Uint32(0) // reserved2
}

// TestNDRRestrictionExistVector pins the node framing independently with a
// hand-built RES_EXIST: it has no referent and no deferred value, so its bytes
// are fully deterministic and anchor the res_type-twice pattern that the rest of
// the recursive codec depends on.
func TestNDRRestrictionExistVector(t *testing.T) {
	buf := []byte{
		0x08, 0x00, 0x00, 0x00, // top res_type = RES_EXIST
		0x08, 0x00, 0x00, 0x00, // union res_type = RES_EXIST
		0x00, 0x00, 0x00, 0x00, // reserved1
		0x1F, 0x00, 0x01, 0x30, // proptag PrDisplayName (0x3001001F)
		0x00, 0x00, 0x00, 0x00, // reserved2
	}
	r, err := pullRestrictionNDR(ndr.NewPull(buf))
	if err != nil {
		t.Fatalf("pull exist restriction: %v", err)
	}
	if r.Type != mapi.ResExist {
		t.Fatalf("type = %#x, want ResExist", r.Type)
	}
	ex, ok := r.Value.(mapi.ExistRestriction)
	if !ok || ex.PropTag != mapi.PrDisplayName {
		t.Errorf("exist restriction = %+v (ok=%v), want PrDisplayName", r.Value, ok)
	}
}

// TestNDRRestrictionProperty pulls a RES_PROPERTY whose deferred PROPERTY_VALUE
// carries a string, the shape a GAL ANR search uses.
func TestNDRRestrictionProperty(t *testing.T) {
	p := ndr.NewPush()
	pushPropResForTest(t, p, mapi.RelopEQ, mapi.PrDisplayName, "alice")
	r, err := pullRestrictionNDR(ndr.NewPull(p.Bytes()))
	if err != nil {
		t.Fatalf("pull property restriction: %v", err)
	}
	pr, ok := r.Value.(mapi.PropertyRestriction)
	if !ok {
		t.Fatalf("value type = %T, want PropertyRestriction", r.Value)
	}
	if pr.Relop != mapi.RelopEQ || pr.PropTag != mapi.PrDisplayName {
		t.Errorf("restriction = {relop %d, tag %#x}, want {EQ, PrDisplayName}", pr.Relop, uint32(pr.PropTag))
	}
	if s, _ := pr.PropVal.Value.(string); s != "alice" {
		t.Errorf("restriction value = %q, want %q", s, "alice")
	}
}

// TestNDRRestrictionAndTree pulls a compound RES_AND of a property and an exist
// restriction, proving the recursion and the children referent/count handling.
func TestNDRRestrictionAndTree(t *testing.T) {
	p := ndr.NewPush()
	p.Uint32(uint32(mapi.ResAnd))
	p.Uint32(uint32(mapi.ResAnd))
	p.Uint32(2)       // cres
	p.UniquePtr(true) // children referent
	p.Uint32(2)       // conformant count == cres
	pushPropResForTest(t, p, mapi.RelopEQ, mapi.PrDisplayName, "alice")
	pushExistResForTest(p, mapi.PrSmtpAddress)

	r, err := pullRestrictionNDR(ndr.NewPull(p.Bytes()))
	if err != nil {
		t.Fatalf("pull and restriction: %v", err)
	}
	if r.Type != mapi.ResAnd {
		t.Fatalf("type = %#x, want ResAnd", r.Type)
	}
	kids, ok := r.Value.([]mapi.Restriction)
	if !ok || len(kids) != 2 {
		t.Fatalf("children = %T len %d, want 2 restrictions", r.Value, len(kids))
	}
	if kids[0].Type != mapi.ResProperty {
		t.Errorf("child 0 type = %#x, want ResProperty", kids[0].Type)
	}
	if kids[1].Type != mapi.ResExist {
		t.Errorf("child 1 type = %#x, want ResExist", kids[1].Type)
	}
	if ex, _ := kids[1].Value.(mapi.ExistRestriction); ex.PropTag != mapi.PrSmtpAddress {
		t.Errorf("child 1 exist tag = %#x, want PrSmtpAddress", uint32(ex.PropTag))
	}
}

// TestNDRRestrictionUnsupported proves a structural restriction kind the GAL
// never receives is a loud error, not a silent wire desync.
func TestNDRRestrictionUnsupported(t *testing.T) {
	buf := []byte{
		0x06, 0x00, 0x00, 0x00, // RES_BITMASK
		0x06, 0x00, 0x00, 0x00,
	}
	if _, err := pullRestrictionNDR(ndr.NewPull(buf)); err == nil {
		t.Error("pull of an unsupported restriction kind succeeded, want error")
	}
}
