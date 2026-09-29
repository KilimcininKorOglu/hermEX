package ews

import (
	"regexp"
	"strings"
	"testing"
)

var displayNameRE = regexp.MustCompile(`<DisplayName>([^<]*)</DisplayName>`)

// findFolderNames runs a deep FindFolder under msgfolderroot with the given
// restriction and returns the response and the names it lists.
func findFolderNames(t *testing.T, restriction string) (string, []string) {
	t.Helper()
	ts, _ := seededWithMessage(t, plainMessage)
	resp, out := soapPost(t, ts, wrapRequest(`<FindFolder Traversal="Deep" xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`">`+
		`<FolderShape><t:BaseShape>Default</t:BaseShape></FolderShape>`+
		`<Restriction>`+restriction+`</Restriction>`+
		`<ParentFolderIds><t:DistinguishedFolderId Id="msgfolderroot"/></ParentFolderIds>`+
		`</FindFolder>`), true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, out)
	}
	var names []string
	for _, m := range displayNameRE.FindAllStringSubmatch(out, -1) {
		names = append(names, m[1])
	}
	return out, names
}

// TestFindFolderAppliesItsRestriction proves FindFolder lists only the folders
// its restriction matches, on a stored property (the name) and on one the store
// computes (the message count).
func TestFindFolderAppliesItsRestriction(t *testing.T) {
	cases := []struct{ name, restriction string }{
		{"by name", `<t:Contains ContainmentMode="Substring" ContainmentComparison="IgnoreCase">` +
			`<t:FieldURI FieldURI="folder:DisplayName"/><t:Constant Value="inb"/></t:Contains>`},
		{"by message count", `<t:IsGreaterThan><t:FieldURI FieldURI="folder:TotalCount"/>` +
			`<t:FieldURIOrConstant><t:Constant Value="0"/></t:FieldURIOrConstant></t:IsGreaterThan>`},
	}
	for _, c := range cases {
		out, names := findFolderNames(t, c.restriction)
		if len(names) != 1 || names[0] != "Inbox" || !strings.Contains(out, `TotalItemsInView="1"`) {
			t.Errorf("%s: FindFolder listed %v, want the Inbox only: %s", c.name, names, out)
		}
	}
}

// TestFindFolderRefusesARestrictionItCannotApply proves a restriction on a
// path the server cannot test is refused rather than dropped, which would list
// every folder.
func TestFindFolderRefusesARestrictionItCannotApply(t *testing.T) {
	out, _ := findFolderNames(t, `<t:Exists><t:FieldURI FieldURI="folder:EffectiveRights"/></t:Exists>`)
	if !strings.Contains(out, "<ResponseCode>ErrorUnsupportedPathForQuery</ResponseCode>") {
		t.Errorf("FindFolder = %s, want ErrorUnsupportedPathForQuery", out)
	}
}
