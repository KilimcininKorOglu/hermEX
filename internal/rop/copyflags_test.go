package rop

import (
	"testing"

	"hermex/internal/mapi"
)

// specInvalidParameter is InvalidParameter 0x80070057 ([MS-OXCDATA] 2.4), the code
// [MS-OXCPRPT] 3.2.5.7 and 3.2.5.8 give for a CopyFlags value with any bit other
// than Move and NoOverwrite set.
const specInvalidParameter uint32 = 0x80070057

// specUnknownCopyFlags are CopyFlags values that carry at least one bit outside
// Move (0x01) and NoOverwrite (0x02), the two flags [MS-OXCROPS] 2.2.8.11.1 and
// 2.2.8.12.1 define: each unknown bit alone, every bit set, and an unknown bit
// combined with each defined one.
var specUnknownCopyFlags = []uint8{0x04, 0x08, 0x10, 0x20, 0x40, 0x80, 0xFF, 0x06, 0x05}

// copyFlagsROP builds one of the two copy ROPs from the source at slot 0 to the
// destination at slot 1 with the given CopyFlags, copying PidTagSubject.
type copyFlagsROP struct {
	name  string
	ropID uint8
	build func(copyFlags uint8) []byte
}

var copyFlagsROPs = []copyFlagsROP{
	{"CopyProperties", 0x67, func(fl uint8) []byte { return buildCopyProperties(0, 1, fl, []mapi.PropTag{mapi.PrSubject}) }},
	{"CopyTo", 0x39, func(fl uint8) []byte { return buildCopyTo(0, 1, fl, nil) }},
}

// newFlagsFixture opens the seeded message (subject FLAGSRC) as the source and a
// compose message holding subject DSTKEEP as the destination.
func newFlagsFixture(t *testing.T) (*copyFixture, uint32, uint32) {
	t.Helper()
	f := newCopyFixture(t, "FLAGSRC")
	srcH := f.openSource(t)
	dstH := f.newCompose(t)
	dispatchOK(t, f.sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "DSTKEEP"}}),
		ropSetProperties, "SetProperties", dstH)
	return f, srcH, dstH
}

// wantHandleSubject asserts the object at h, read through its handle so a
// buffered write counts, carries subject want.
func wantHandleSubject(t *testing.T, sess *Session, h uint32, want, label string) {
	t.Helper()
	obj := sess.get(h)
	if obj == nil {
		t.Fatalf("%s: handle %d resolves to no object", label, h)
	}
	props, ok := sess.objectAllProps(obj)
	if !ok {
		t.Fatalf("%s: the object's properties could not be read", label)
	}
	wantProp(t, props, mapi.PrSubject, want, label)
}

// TestCopyRefusesUnknownCopyFlags sends every unknown CopyFlags value through both
// copy ROPs between valid handles. Each answers InvalidParameter with no body, and
// neither the destination nor the source changes ([MS-OXCPRPT] 3.2.5.7, 3.2.5.8).
func TestCopyRefusesUnknownCopyFlags(t *testing.T) {
	for _, r := range copyFlagsROPs {
		t.Run(r.name, func(t *testing.T) {
			f, srcH, dstH := newFlagsFixture(t)
			for _, fl := range specUnknownCopyFlags {
				label := r.name + " CopyFlags " + hexByte(fl)
				wantCopyFailure(t, mustDispatch(f.sess, r.build(fl), srcH, dstH), r.ropID, specInvalidParameter, label)
				wantHandleSubject(t, f.sess, dstH, "DSTKEEP", label+" destination")
				wantHandleSubject(t, f.sess, srcH, "FLAGSRC", label+" source handle")
			}
			wantProp(t, mustMessageProps(t, f.store, int64(f.mid), mapi.PrSubject), mapi.PrSubject, "FLAGSRC",
				r.name+" stored source after the refusals")
		})
	}
}

// TestCopyAcceptsDefinedCopyFlags is the control for the refusal test: no flag
// copies over the destination's value, NoOverwrite 0x02 keeps it ([MS-OXCPRPT]
// 3.2.5.7, 3.2.5.8).
func TestCopyAcceptsDefinedCopyFlags(t *testing.T) {
	cases := []struct {
		flags uint8
		want  string
	}{
		{0x00, "FLAGSRC"},
		{0x02, "DSTKEEP"},
	}
	for _, r := range copyFlagsROPs {
		for _, c := range cases {
			f, srcH, dstH := newFlagsFixture(t)
			label := r.name + " CopyFlags " + hexByte(c.flags)
			wantCopySuccess(t, mustDispatch(f.sess, r.build(c.flags), srcH, dstH), r.ropID, label)
			wantHandleSubject(t, f.sess, dstH, c.want, label+" destination")
		}
	}
}

// hexByte renders b as 0xNN for a test label.
func hexByte(b uint8) string {
	const digits = "0123456789ABCDEF"
	return "0x" + string([]byte{digits[b>>4], digits[b&0x0F]})
}
