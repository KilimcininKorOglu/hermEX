package ews

import (
	"encoding/xml"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// updateCalendarPermsReq is an UpdateFolder of the calendar's permissions as a
// client sends them: a CalendarPermissionSet inside a t:CalendarFolder.
func updateCalendarPermsReq(members string) string {
	return wrapRequest(`<UpdateFolder xmlns="` + nsMessages + `">` +
		`<FolderChanges><t:FolderChange xmlns:t="` + nsTypes + `">` +
		`<t:DistinguishedFolderId Id="calendar"/>` +
		`<t:Updates><t:SetFolderField><t:FieldURI FieldURI="folder:PermissionSet"/>` +
		`<t:CalendarFolder><t:PermissionSet><t:CalendarPermissions>` + members + `</t:CalendarPermissions></t:PermissionSet></t:CalendarFolder>` +
		`</t:SetFolderField></t:Updates></t:FolderChange></FolderChanges></UpdateFolder>`)
}

// calendarMember is one CalendarPermission at a canned level.
func calendarMember(user, level string) string {
	return `<t:CalendarPermission><t:UserId>` + user + `</t:UserId>` +
		`<t:CalendarPermissionLevel>` + level + `</t:CalendarPermissionLevel></t:CalendarPermission>`
}

// calendarPermsResp reads a GetFolder response's CalendarPermissionSet, keyed by
// member to its level and read access.
func calendarPermsResp(t *testing.T, out string) map[string][2]string {
	t.Helper()
	var env struct {
		Perms []struct {
			UserID struct {
				PrimarySmtpAddress string `xml:"PrimarySmtpAddress"`
				DistinguishedUser  string `xml:"DistinguishedUser"`
			} `xml:"UserId"`
			ReadItems string `xml:"ReadItems"`
			Level     string `xml:"CalendarPermissionLevel"`
		} `xml:"Body>GetFolderResponse>ResponseMessages>GetFolderResponseMessage>Folders>CalendarFolder>PermissionSet>CalendarPermissions>CalendarPermission"`
	}
	if err := xml.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	got := map[string][2]string{}
	for _, p := range env.Perms {
		key := p.UserID.DistinguishedUser + p.UserID.PrimarySmtpAddress
		got[key] = [2]string{p.Level, p.ReadItems}
	}
	return got
}

// TestCalendarPermissionsRoundTrip proves a calendar's permissions are written
// and read as a CalendarPermissionSet, the form EWS gives a calendar folder, and
// that the free/busy levels, which only a calendar has, keep their meaning: a
// member who may see free/busy time only is stored with that right alone.
func TestCalendarPermissionsRoundTrip(t *testing.T) {
	ts, dir := seededEWS(t)
	members := calendarMember(`<t:DistinguishedUser>Default</t:DistinguishedUser>`, "FreeBusyTimeOnly") +
		calendarMember(`<t:PrimarySmtpAddress>alice@hermex.test</t:PrimarySmtpAddress>`, "Reviewer") +
		calendarMember(`<t:DistinguishedUser>Anonymous</t:DistinguishedUser>`, "None")
	if _, out := soapPost(t, ts, updateCalendarPermsReq(members), true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateFolder = %s", out)
	}
	stored := folderPerms(t, dir, int64(mapi.PrivateFIDCalendar))
	if stored["default"] != mapi.FrightsFreeBusySimple {
		t.Errorf("default rights = %#x, want free/busy time only %#x", stored["default"], mapi.FrightsFreeBusySimple)
	}
	_, out := soapPost(t, ts, getFolderPermsReq(`<t:DistinguishedFolderId Id="calendar" xmlns:t="`+nsTypes+`"/>`), true)
	got := calendarPermsResp(t, out)
	want := map[string][2]string{
		"Default":           {"FreeBusyTimeOnly", "TimeOnly"},
		"alice@hermex.test": {"Reviewer", "FullDetails"},
		"Anonymous":         {"None", "None"},
	}
	for member, w := range want {
		if got[member] != w {
			t.Errorf("%s = %v, want %v: %s", member, got[member], w, out)
		}
	}
	if strings.Contains(out, "<PermissionLevel>") {
		t.Errorf("a calendar member carries a PermissionLevel: %s", out)
	}
}
