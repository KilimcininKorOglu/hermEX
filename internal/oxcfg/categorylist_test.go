package oxcfg

import (
	"strings"
	"testing"
)

const outlookList = `<?xml version="1.0"?>
<categories default="Red category" lastSavedSession="7">
  <category name="Red category" color="0" keyboardShortcut="1" usageCount="12"
            lastTimeUsedMail="2026-01-02T03:04:05" lastTimeUsed="2026-01-02T03:04:05"
            lastSessionUsed="7" guid="{11111111-2222-3333-4444-555555555555}"/>
  <category name="Project X" color="7" usageCount="3"
            guid="{66666666-7777-8888-9999-000000000000}"/>
</categories>`

// TestDecodeOutlookList reads the shape Outlook writes.
func TestDecodeOutlookList(t *testing.T) {
	l, err := Decode([]byte(outlookList))
	if err != nil {
		t.Fatal(err)
	}
	if l.Default != "Red category" || l.LastSaved != "7" {
		t.Errorf("document attributes = %q / %q", l.Default, l.LastSaved)
	}
	if len(l.Categories) != 2 {
		t.Fatalf("got %d categories, want 2", len(l.Categories))
	}
}

// TestDecodeCategoryAttributes pins the per-category fields, including the
// bookkeeping hermEX does not use but must carry.
func TestDecodeCategoryAttributes(t *testing.T) {
	l, err := Decode([]byte(outlookList))
	if err != nil {
		t.Fatal(err)
	}
	c := l.Categories[0]
	if c.Name != "Red category" || c.Color != 0 || c.GUID != "{11111111-2222-3333-4444-555555555555}" {
		t.Errorf("first category = %+v", c)
	}
	if c.KeyboardShortcut != "1" || c.UsageCount != "12" || c.LastSessionUsed != "7" {
		t.Errorf("bookkeeping lost: %+v", c)
	}
}

// TestReencodePreservesOutlooksBookkeeping is the interop guarantee. Webmail edits
// a name or a colour; every other attribute belongs to Outlook and dropping it
// costs the user their keyboard shortcuts and usage ordering on the first save.
func TestReencodePreservesOutlooksBookkeeping(t *testing.T) {
	l, err := Decode([]byte(outlookList))
	if err != nil {
		t.Fatal(err)
	}
	l.Categories[1].Color = 4 // the only edit

	out, err := Encode(l)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		`guid="{11111111-2222-3333-4444-555555555555}"`,
		`keyboardShortcut="1"`,
		`usageCount="12"`,
		`lastSessionUsed="7"`,
		`lastTimeUsedMail="2026-01-02T03:04:05"`, // not modelled, carried anyway
		`default="Red category"`,
		`lastSavedSession="7"`,
		`color="4"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("re-encoded list dropped %s\n%s", want, got)
		}
	}
}

// TestEncodeIsStable keeps an unchanged list encoding to the same bytes, so a save
// that changed nothing does not look like an edit to the next reader.
func TestEncodeIsStable(t *testing.T) {
	l, err := Decode([]byte(outlookList))
	if err != nil {
		t.Fatal(err)
	}
	first, err := Encode(l)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Decode(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("re-encode is not stable:\n%s\n%s", first, second)
	}
}

// TestDecodeEmpty treats an absent or blank stream as an empty list, which is what
// a mailbox that has never had a category list looks like.
func TestDecodeEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\n"} {
		l, err := Decode([]byte(in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if len(l.Categories) != 0 {
			t.Errorf("%q gave %d categories", in, len(l.Categories))
		}
	}
}

// TestDecodeRejectsGarbage keeps a corrupt stream from reading as an empty list,
// because seeding over it would destroy a list Outlook still holds.
func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode([]byte("<categories><category name=")); err == nil {
		t.Error("a truncated document decoded without error")
	}
}

// TestPaletteRoundTrip pins the colour mapping in both directions.
func TestPaletteRoundTrip(t *testing.T) {
	for i, hex := range PaletteHex {
		if got := NearestPalette(hex); got != i {
			t.Errorf("NearestPalette(%s) = %d, want %d", hex, got, i)
		}
		if got := HexForPalette(i); got != hex {
			t.Errorf("HexForPalette(%d) = %s, want %s", i, got, hex)
		}
	}
}

// TestNearestPaletteSnaps documents the lossy step: a colour outside the palette
// takes the closest entry rather than being stored as itself.
func TestNearestPaletteSnaps(t *testing.T) {
	if got := NearestPalette("#000001"); got != 14 { // black
		t.Errorf("near-black snapped to %d (%s), want 14", got, HexForPalette(got))
	}
	if got := NearestPalette("not a colour"); got != 0 {
		t.Errorf("an unparseable colour gave %d, want 0", got)
	}
	if got := HexForPalette(99); got != PaletteHex[0] {
		t.Errorf("an out-of-range index gave %s", got)
	}
}
