package nspi

import (
	"errors"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// pushNotHeader writes one NOT frame: the doubled discriminant plus a non-zero
// referent for the child that follows.
func pushNotHeader(p *ndr.Push) {
	p.Uint32(uint32(mapi.ResNot))
	p.Uint32(uint32(mapi.ResNot))
	p.UniquePtr(true)
}

// TestNDRRestrictionRejectsImpossibleChildCount is the allocation guard. The
// child count is a 32-bit field the transport's body cap does not bound, so a
// twenty-byte request could ask for a slice of two billion restrictions. The
// count has to be refused against the bytes that remain before it becomes a
// make() length; otherwise the decoder reserves tens of gigabytes and the shared
// RPC daemon dies of an unrecoverable out-of-memory throw.
func TestNDRRestrictionRejectsImpossibleChildCount(t *testing.T) {
	p := ndr.NewPush()
	p.Uint32(uint32(mapi.ResAnd))
	p.Uint32(uint32(mapi.ResAnd))
	p.Uint32(0x7FFFFFFF) // cres
	p.UniquePtr(true)    // children referent
	p.Uint32(0x7FFFFFFF) // conformant count == cres

	_, err := pullRestrictionNDR(ndr.NewPull(p.Bytes()))
	if !errors.Is(err, ndr.ErrUnderflow) {
		t.Fatalf("impossible child count gave %v, want the underflow refusal before allocation", err)
	}
}

// TestNDRRestrictionRejectsDeepNesting is the recursion guard. A NOT node costs
// twelve wire bytes and recurses once, so an unbounded decoder recurses once per
// few input bytes and a large filter overflows the goroutine stack, which is a
// runtime throw no caller can recover from.
func TestNDRRestrictionRejectsDeepNesting(t *testing.T) {
	p := ndr.NewPush()
	for range maxRestrictionDepth + 5 {
		pushNotHeader(p)
	}
	pushExistResForTest(p, mapi.PrSmtpAddress)

	_, err := pullRestrictionNDR(ndr.NewPull(p.Bytes()))
	if err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("deep NOT chain gave %v, want the depth-limit refusal", err)
	}
}

// TestNDRRestrictionKeepsLegitimateNesting is the control: the cap sits far above
// any real filter, so ordinary nesting still decodes.
func TestNDRRestrictionKeepsLegitimateNesting(t *testing.T) {
	p := ndr.NewPush()
	pushNotHeader(p)
	pushNotHeader(p)
	pushExistResForTest(p, mapi.PrSmtpAddress)

	r, err := pullRestrictionNDR(ndr.NewPull(p.Bytes()))
	if err != nil {
		t.Fatalf("ordinary nesting refused: %v", err)
	}
	if r.Type != mapi.ResNot {
		t.Fatalf("type = %#x, want ResNot", r.Type)
	}
}
