package ews

import (
	"strings"
	"testing"
)

// headerMessage carries a folded header, so the test sees it served as one field.
const headerMessage = "From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: headers\r\n" +
	"X-Folded: first\r\n second\r\nMessage-ID: <hdr@hermex.test>\r\n" +
	"Content-Type: text/plain\r\n\r\nbody\r\n"

// getItemWithShape fetches the seeded Inbox message with the given base shape and
// additional field URIs.
func getItemWithShape(t *testing.T, base, fields string) string {
	t.Helper()
	ts, _ := seededWithMessage(t, headerMessage)
	_, found := soapPost(t, ts, wrapRequest(`<FindItem Traversal="Shallow" xmlns="`+nsMessages+`"><ItemShape><BaseShape>IdOnly</BaseShape></ItemShape>`+
		`<ParentFolderIds><t:DistinguishedFolderId Id="inbox" xmlns:t="`+nsTypes+`"/></ParentFolderIds></FindItem>`), true)
	itemID := itemIDRE.FindStringSubmatch(found)
	if len(itemID) != 2 {
		t.Fatalf("FindItem returned no ItemId: %s", found)
	}
	_, got := soapPost(t, ts, wrapRequest(`<GetItem xmlns="`+nsMessages+`"><ItemShape><BaseShape>`+base+`</BaseShape>`+fields+`</ItemShape>`+
		`<ItemIds><t:ItemId Id="`+itemID[1]+`" xmlns:t="`+nsTypes+`"/></ItemIds></GetItem>`), true)
	return got
}

// TestGetItemServesInternetMessageHeaders proves GetItem returns the message's
// header block when the shape names item:InternetMessageHeaders or asks for
// AllProperties, each field under its HeaderName with a folded value unfolded, and
// leaves the headers out of a shape that does not ask for them.
func TestGetItemServesInternetMessageHeaders(t *testing.T) {
	named := `<AdditionalProperties xmlns:t="` + nsTypes + `"><t:FieldURI FieldURI="item:InternetMessageHeaders"/></AdditionalProperties>`
	for name, out := range map[string]string{
		"FieldURI":      getItemWithShape(t, "Default", named),
		"AllProperties": getItemWithShape(t, "AllProperties", ""),
	} {
		for _, want := range []string{
			`<InternetMessageHeader HeaderName="Subject">headers</InternetMessageHeader>`,
			`<InternetMessageHeader HeaderName="X-Folded">first second</InternetMessageHeader>`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: missing %s in %s", name, want, out)
			}
		}
		if i, j := strings.Index(out, "<Importance>"), strings.Index(out, "<InternetMessageHeaders>"); i < 0 || j < i || j > strings.Index(out, "<HasAttachments>") {
			t.Errorf("%s: InternetMessageHeaders is not between Importance and HasAttachments: %s", name, out)
		}
	}
	if out := getItemWithShape(t, "Default", ""); strings.Contains(out, "InternetMessageHeader") {
		t.Errorf("the default shape served the headers: %s", out)
	}
}
