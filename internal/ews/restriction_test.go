package ews

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// restrictedMailbox seeds the Inbox with three messages: an urgent report, a
// normal report that has been read, and a lunch invitation.
func restrictedMailbox(t *testing.T) *httptest.Server {
	t.Helper()
	mail := func(subject, extra string) string {
		return "From: Bob <bob@hermex.test>\r\nTo: Alice <alice@hermex.test>\r\nSubject: " + subject + "\r\n" +
			extra + "Date: Wed, 12 Jun 2024 13:46:40 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n"
	}
	ts, dir := seededWithMessage(t,
		mail("Quarterly report", "Importance: High\r\n"),
		mail("Monthly report", ""),
		mail("Lunch on Friday", ""))
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Subject == "Monthly report" {
			if err := st.SetMessageFlags(int64(mapi.PrivateFIDInbox), m.UID, objectstore.FlagSeen); err != nil {
				t.Fatal(err)
			}
		}
	}
	return ts
}

// findWith runs a FindItem on the Inbox with a restriction.
func findWith(t *testing.T, ts *httptest.Server, restriction string) string {
	t.Helper()
	_, out := soapPost(t, ts, wrapRequest(`<FindItem Traversal="Shallow" xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`">`+
		`<ItemShape><t:BaseShape>Default</t:BaseShape></ItemShape>`+
		`<Restriction>`+restriction+`</Restriction>`+
		`<ParentFolderIds><t:DistinguishedFolderId Id="inbox"/></ParentFolderIds>`+
		`</FindItem>`), true)
	return out
}

// subjectsIn returns which of the seeded subjects a response lists.
func subjectsIn(out string) []string {
	var got []string
	for _, s := range []string{"Quarterly report", "Monthly report", "Lunch on Friday"} {
		if strings.Contains(out, "<Subject>"+s+"</Subject>") {
			got = append(got, s)
		}
	}
	return got
}

// TestFindItemAppliesItsRestriction proves FindItem lists only the items its
// restriction matches, for each kind of search expression a client sends.
func TestFindItemAppliesItsRestriction(t *testing.T) {
	ts := restrictedMailbox(t)
	cases := []struct {
		name, restriction string
		want              []string
	}{
		{"substring ignoring case",
			`<t:Contains ContainmentMode="Substring" ContainmentComparison="IgnoreCase"><t:FieldURI FieldURI="item:Subject"/><t:Constant Value="REPORT"/></t:Contains>`,
			[]string{"Quarterly report", "Monthly report"}},
		{"full string is the default",
			`<t:Contains><t:FieldURI FieldURI="item:Subject"/><t:Constant Value="report"/></t:Contains>`,
			nil},
		{"prefix of a word",
			`<t:Contains ContainmentMode="PrefixOnWords"><t:FieldURI FieldURI="item:Subject"/><t:Constant Value="Fri"/></t:Contains>`,
			[]string{"Lunch on Friday"}},
		{"importance by name",
			`<t:IsEqualTo><t:FieldURI FieldURI="item:Importance"/><t:FieldURIOrConstant><t:Constant Value="High"/></t:FieldURIOrConstant></t:IsEqualTo>`,
			[]string{"Quarterly report"}},
		{"unread items",
			`<t:IsEqualTo><t:FieldURI FieldURI="message:IsRead"/><t:FieldURIOrConstant><t:Constant Value="false"/></t:FieldURIOrConstant></t:IsEqualTo>`,
			[]string{"Quarterly report", "Lunch on Friday"}},
		{"sender by address",
			`<t:Contains ContainmentMode="Substring" ContainmentComparison="IgnoreCase"><t:FieldURI FieldURI="message:From"/><t:Constant Value="bob@hermex"/></t:Contains>`,
			[]string{"Quarterly report", "Monthly report", "Lunch on Friday"}},
		{"not and or",
			`<t:And><t:Not><t:Contains ContainmentMode="Substring"><t:FieldURI FieldURI="item:Subject"/><t:Constant Value="Lunch"/></t:Contains></t:Not>` +
				`<t:Or><t:IsEqualTo><t:FieldURI FieldURI="item:Importance"/><t:FieldURIOrConstant><t:Constant Value="High"/></t:FieldURIOrConstant></t:IsEqualTo>` +
				`<t:IsEqualTo><t:FieldURI FieldURI="message:IsRead"/><t:FieldURIOrConstant><t:Constant Value="true"/></t:FieldURIOrConstant></t:IsEqualTo></t:Or></t:And>`,
			[]string{"Quarterly report", "Monthly report"}},
		{"read bit clear, by extended property",
			`<t:Excludes><t:ExtendedFieldURI PropertyTag="0x0E07" PropertyType="Integer"/><t:Bitmask Value="0x1"/></t:Excludes>`,
			[]string{"Quarterly report", "Lunch on Friday"}},
		{"received after a time",
			`<t:IsGreaterThan><t:FieldURI FieldURI="item:DateTimeReceived"/><t:FieldURIOrConstant><t:Constant Value="` +
				time.Unix(1718200000, 0).Add(-time.Hour).UTC().Format(time.RFC3339) + `"/></t:FieldURIOrConstant></t:IsGreaterThan>`,
			[]string{"Quarterly report", "Monthly report", "Lunch on Friday"}},
		{"a property no item carries",
			`<t:Exists><t:ExtendedFieldURI DistinguishedPropertySetId="PublicStrings" PropertyName="nothing" PropertyType="String"/></t:Exists>`,
			nil},
	}
	for _, c := range cases {
		out := findWith(t, ts, c.restriction)
		if !strings.Contains(out, `ResponseClass="Success"`) {
			t.Errorf("%s: FindItem failed: %s", c.name, out)
			continue
		}
		if got := subjectsIn(out); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: listed %v, want %v", c.name, got, c.want)
		}
	}
}

// TestFindItemRefusesARestrictionItCannotApply proves a filter the server cannot
// apply is refused, never dropped: a client told its search succeeded would
// show items it asked to leave out.
func TestFindItemRefusesARestrictionItCannotApply(t *testing.T) {
	ts := restrictedMailbox(t)
	cases := []struct{ name, restriction, code string }{
		{"unknown field",
			`<t:Exists><t:FieldURI FieldURI="item:NoSuchField"/></t:Exists>`, "ErrorUnsupportedPathForQuery"},
		{"constant of the wrong type",
			`<t:IsEqualTo><t:FieldURI FieldURI="item:Size"/><t:FieldURIOrConstant><t:Constant Value="big"/></t:FieldURIOrConstant></t:IsEqualTo>`,
			"ErrorInvalidRestriction"},
		{"unknown importance",
			`<t:IsEqualTo><t:FieldURI FieldURI="item:Importance"/><t:FieldURIOrConstant><t:Constant Value="Urgent"/></t:FieldURIOrConstant></t:IsEqualTo>`,
			"ErrorInvalidRestriction"},
		{"unknown containment mode",
			`<t:Contains ContainmentMode="Fuzzy"><t:FieldURI FieldURI="item:Subject"/><t:Constant Value="x"/></t:Contains>`,
			"ErrorInvalidRestriction"},
		{"contains on a number",
			`<t:Contains><t:FieldURI FieldURI="item:Size"/><t:Constant Value="1"/></t:Contains>`, "ErrorInvalidRestriction"},
		{"unknown expression", `<t:Resembles/>`, "ErrorInvalidRestriction"},
		{"empty and", `<t:And/>`, "ErrorInvalidRestriction"},
	}
	for _, c := range cases {
		out := findWith(t, ts, c.restriction)
		if !strings.Contains(out, "<ResponseCode>"+c.code+"</ResponseCode>") || len(subjectsIn(out)) != 0 {
			t.Errorf("%s: FindItem = %s, want %s and no items", c.name, out, c.code)
		}
	}
}
