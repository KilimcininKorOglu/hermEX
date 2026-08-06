package ext

import (
	"encoding/binary"
	"runtime"
	"testing"

	"hermex/internal/mapi"
)

// allocBudget is the most a decoder may allocate while refusing a bogus count.
// An error alone proves nothing here: the unbounded decoders also failed, but only
// after reserving the whole array and then running out of bytes to fill it. What
// the bound has to change is that the reservation never happens.
const allocBudget = 64 << 10

// assertRefusedCheaply runs decode and requires both that it reports an error and
// that it did so without allocating from the count it was given.
func assertRefusedCheaply(t *testing.T, name string, decode func(*Pull) error, body []byte) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := decode(NewPull(body, FlagUTF16))
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Errorf("%s accepted a count it could not possibly satisfy", name)
	}
	if used := after.TotalAlloc - before.TotalAlloc; used > allocBudget {
		t.Errorf("%s allocated %d bytes before refusing; the count was never checked against the buffer", name, used)
	}
}

// hugeCount is a wire buffer holding nothing but a 32-bit count of 0xFFFFFFFF:
// four bytes asking a decoder to reserve billions of elements. For an EID array
// that is roughly 34 GB, for a GUID multivalue roughly 68 GB. The count field is
// independent of the transport's buffer cap, so nothing upstream bounds it.
func hugeCount() []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, 0xFFFFFFFF)
	return b
}

// hugeShortCount is the 16-bit equivalent: a count of 65535 with no elements
// behind it.
func hugeShortCount() []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, 0xFFFF)
	return b
}

// TestWideCountsAreBoundedByTheBuffer proves every wide-count decoder refuses a
// count it could not possibly satisfy, instead of allocating from it first. Each
// of these is reachable from an authenticated MAPI/HTTP or NSPI session, where an
// out-of-memory fatal error would kill the daemon serving every other session.
func TestWideCountsAreBoundedByTheBuffer(t *testing.T) {
	cases := map[string]func(*Pull) error{
		"PropTagsLong": func(p *Pull) error {
			_, err := p.PropTagsLong()
			return err
		},
		"PropertyValuesLong": func(p *Pull) error {
			_, err := p.PropertyValuesLong()
			return err
		},
		"TArraySet": func(p *Pull) error {
			_, err := p.TArraySet()
			return err
		},
		"EIDs": func(p *Pull) error {
			_, err := p.EIDs()
			return err
		},
	}
	for name, decode := range cases {
		t.Run(name, func(t *testing.T) { assertRefusedCheaply(t, name, decode, hugeCount()) })
	}
}

// TestMultivalueCountIsBoundedByTheBuffer covers the pullMV path, which every
// PT_MV_* property goes through. It is reached by RopSetProperties decoding a
// client-supplied property blob, so any authenticated session can drive it.
func TestMultivalueCountIsBoundedByTheBuffer(t *testing.T) {
	for _, typ := range []mapi.PropType{
		mapi.PtMvLong, mapi.PtMvI8, mapi.PtMvCLSID, mapi.PtMvUnicode, mapi.PtMvBinary,
	} {
		decode := func(p *Pull) error {
			_, err := p.PropValue(typ)
			return err
		}
		assertRefusedCheaply(t, "multivalue", decode, hugeCount())
	}
}

// TestRestrictionCountIsBoundedByTheBuffer covers the AND/OR branch, reached by
// the NSPI address-book search filter, which is parsed with the wide-count flag
// set from a client-supplied body.
func TestRestrictionCountIsBoundedByTheBuffer(t *testing.T) {
	// The wide-count flag is what the NSPI filter parser sets, so it is checked
	// through a Pull built with it rather than through the shared helper.
	var before, after runtime.MemStats
	body := append([]byte{byte(mapi.ResAnd)}, hugeCount()...)
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := NewPull(body, FlagWCount).Restriction()
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Error("a wide-count AND restriction accepted a count of 4 billion from a 5-byte buffer")
	}
	if used := after.TotalAlloc - before.TotalAlloc; used > allocBudget {
		t.Errorf("the restriction decoder allocated %d bytes before refusing", used)
	}

	assertRefusedCheaply(t, "short-count OR restriction", func(p *Pull) error {
		_, err := p.Restriction()
		return err
	}, append([]byte{byte(mapi.ResOr)}, hugeShortCount()...))
}

// TestShortCountsAreBoundedByTheBuffer covers the 16-bit-counted decoders. Their
// ceiling is lower, but a request that reserves megabytes from two bytes is still
// a request the decoder should refuse rather than serve.
func TestShortCountsAreBoundedByTheBuffer(t *testing.T) {
	cases := map[string]func(*Pull) error{
		"PropertyValues": func(p *Pull) error {
			_, err := p.PropertyValues()
			return err
		},
		"PropTags": func(p *Pull) error {
			_, err := p.PropTags()
			return err
		},
		"PropertyNames": func(p *Pull) error {
			_, err := p.PropertyNames()
			return err
		},
		"PropIDs": func(p *Pull) error {
			_, err := p.PropIDs()
			return err
		},
		"Uint64ArrayShort": func(p *Pull) error {
			_, err := p.Uint64ArrayShort()
			return err
		},
		"ProblemArray": func(p *Pull) error {
			_, err := p.ProblemArray()
			return err
		},
	}
	for name, decode := range cases {
		t.Run(name, func(t *testing.T) { assertRefusedCheaply(t, name, decode, hugeShortCount()) })
	}
}

// TestCountBoundAdmitsRealArrays is the control: the bound must not reject a
// well-formed array. Every element costs at least a byte on this wire, so a
// genuine count never exceeds what remains.
func TestCountBoundAdmitsRealArrays(t *testing.T) {
	push := NewPush(FlagUTF16)
	if err := push.EIDs([]mapi.EID{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	ids, err := NewPull(push.Bytes(), FlagUTF16).EIDs()
	if err != nil {
		t.Fatalf("a well-formed EID array was rejected: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("decoded %d ids, want 3", len(ids))
	}

	push = NewPush(FlagUTF16)
	if err := push.PropTagsLong([]mapi.PropTag{mapi.PrSubject, mapi.PrBody}); err != nil {
		t.Fatal(err)
	}
	tags, err := NewPull(push.Bytes(), FlagUTF16).PropTagsLong()
	if err != nil {
		t.Fatalf("a well-formed tag array was rejected: %v", err)
	}
	if len(tags) != 2 {
		t.Errorf("decoded %d tags, want 2", len(tags))
	}
}
