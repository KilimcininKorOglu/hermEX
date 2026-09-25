package oxvcard

import (
	"slices"
	"testing"

	"hermex/internal/mapi"
)

// fullVCard fills every field Import maps, including each telephone type, the
// three address kinds, all three email slots and an inline photo.
const fullVCard = "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ada Lovelace\r\n" +
	"N:Lovelace;Ada;Augusta;Ms.;Jr.\r\nNICKNAME:Countess\r\nBDAY:1815-12-10\r\n" +
	"TITLE:Mathematician\r\nROLE:Analyst\r\nORG:Analytical Engine;Research\r\nNOTE:First programmer.\r\n" +
	"URL;TYPE=home:https://home.test\r\nURL;TYPE=work:https://work.test\r\n" +
	"TEL;TYPE=fax,home:1\r\nTEL;TYPE=fax,work:2\r\nTEL;TYPE=cell:3\r\nTEL;TYPE=pager:4\r\n" +
	"TEL;TYPE=car:5\r\nTEL;TYPE=home:6\r\nTEL;TYPE=work:7\r\nTEL;TYPE=text:8\r\n" +
	"ADR;TYPE=home:PO 1;;1 Mill St;London;LDN;EC1;UK\r\n" +
	"ADR;TYPE=work:PO 2;;2 Engine Rd;London;LDN;EC2;UK\r\n" +
	"ADR:PO 3;;3 Other Way;Leeds;WYK;LS1;UK\r\n" +
	"EMAIL:a@one.test\r\nEMAIL:a@two.test\r\nEMAIL:a@three.test\r\n" +
	"IMPP:xmpp:ada@chat.test\r\nCATEGORIES:science,history\r\n" +
	"PHOTO:data:image/png;base64,iVBORw0KGgo=\r\nUID:ada-0001\r\nEND:VCARD\r\n"

// TestManagedTagsAreWhatImportWrites holds ManagedTags equal to the tags a full
// card produces. A field added to Import without a place in the set leaves a
// stale value behind on every edit that drops it; a tag in the set that Import
// never writes deletes what another client stored.
func TestManagedTagsAreWhatImportWrites(t *testing.T) {
	r := newResolver()
	opt := Options{Resolver: r.resolve}
	msg, err := Import([]byte(fullVCard), opt)
	mustNoErr(t, err, "import")
	written := map[mapi.PropTag]bool{}
	for _, pv := range msg.Props {
		written[pv.Tag] = true
	}
	managed, err := ManagedTags(opt)
	mustNoErr(t, err, "managed tags")
	for tag := range written {
		if !slices.Contains(managed, tag) {
			t.Errorf("Import writes %#x, which ManagedTags leaves out", uint32(tag))
		}
	}
	for _, tag := range managed {
		if !written[tag] {
			t.Errorf("ManagedTags holds %#x, which the full card does not write", uint32(tag))
		}
	}
}

// TestManagedTagsKeepFileAs leaves the file-as name to the clients that set it,
// and allocates no named property while answering.
func TestManagedTagsKeepFileAs(t *testing.T) {
	r := newResolver()
	opt := Options{Resolver: r.resolve}
	if _, err := namedTags(opt, true); err != nil {
		t.Fatal(err)
	}
	allocated := len(r.ids)
	managed, err := ManagedTags(opt)
	mustNoErr(t, err, "managed tags")
	ids, _ := r.resolve(false, []mapi.PropertyName{mapi.NameFileAs})
	fileAs := mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtUnicode))
	if slices.Contains(managed, fileAs) {
		t.Error("ManagedTags holds the file-as name")
	}
	if len(r.ids) != allocated {
		t.Errorf("ManagedTags allocated %d named properties, want 0", len(r.ids)-allocated)
	}
}
