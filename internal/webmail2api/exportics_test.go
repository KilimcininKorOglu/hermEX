package webmail2api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// icsInvite is a multipart message carrying a text/calendar REQUEST whose
// SUMMARY is summary.
func icsInvite(summary string) string {
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:evt-123@test\r\nSUMMARY:" + summary + "\r\n" +
		"DTSTART:20260101T090000Z\r\nDTEND:20260101T093000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	return "From: org@b.test\r\nSubject: Invite\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=bb\r\n\r\n" +
		"--bb\r\nContent-Type: text/plain\r\n\r\nPlease join.\r\n\r\n" +
		"--bb\r\nContent-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\n" +
		ics + "\r\n--bb--\r\n"
}

// fileICSMail files raw in the inbox and returns its webmail id.
func fileICSMail(t *testing.T, dir, raw string) string {
	t.Helper()
	info, err := openMailbox(t, dir).AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	mustNoErr(t, "append", err)
	return "inbox:" + strconv.FormatUint(uint64(info.UID), 10)
}

// TestExportICS proves GET /mail/export-ics streams a message's embedded
// text/calendar part verbatim as an .ics download (RFC 5546: the iTIP METHOD is
// kept), derives the filename from SUMMARY, and 404s for a message with no invite.
func TestExportICS(t *testing.T) {
	dir := freshMailbox(t)
	uInvite := fileICSMail(t, dir, icsInvite("Team Sync"))
	uPlain := fileICSMail(t, dir, "From: x@b.test\r\nSubject: no invite\r\n\r\njust text\r\n")
	do := apiHarnessFor(t, dir)

	rec := do(http.MethodGet, "/api/v1/mail/export-ics?id="+uInvite, "")
	wantStatus(t, "export invite", rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Errorf("Content-Type = %q, want text/calendar", ct)
	}
	wantContains(t, "Content-Disposition", rec.Header().Get("Content-Disposition"), "Team_Sync.ics")
	wantContains(t, "exported ics", rec.Body.String(), "METHOD:REQUEST")
	wantContains(t, "exported ics", rec.Body.String(), "UID:evt-123@test")

	wantStatus(t, "plain message export", do(http.MethodGet, "/api/v1/mail/export-ics?id="+uPlain, ""), http.StatusNotFound)
}

// TestExportICSRefusesBadIDsAndKeepsTheFilenameSafe: an id that is no id or
// names no message exports nothing, and a SUMMARY carrying quotes, slashes and
// a header-breaking value cannot reach the Content-Disposition filename as is
// (RFC 6266: the filename is a quoted-string the sender must not break out of).
func TestExportICSRefusesBadIDsAndKeepsTheFilenameSafe(t *testing.T) {
	dir := freshMailbox(t)
	id := fileICSMail(t, dir, icsInvite(`..\/evil"; filename=x.exe`))
	do := apiHarnessFor(t, dir)

	for _, bad := range []string{"", "not-an-id", "inbox:999999", "nosuchfolder:1"} {
		if rec := do(http.MethodGet, "/api/v1/mail/export-ics?id="+bad, ""); rec.Code == http.StatusOK {
			t.Errorf("export of %q answered 200", bad)
		}
	}

	rec := do(http.MethodGet, "/api/v1/mail/export-ics?id="+id, "")
	wantStatus(t, "export", rec, http.StatusOK)
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasSuffix(cd, `.ics"`) {
		t.Errorf("Content-Disposition %q does not end in the .ics filename", cd)
	}
	for _, banned := range []string{"/", `\`, ".."} {
		if strings.Contains(cd, banned) {
			t.Errorf("Content-Disposition %q carries %q from the sender's SUMMARY", cd, banned)
		}
	}
	if strings.Count(cd, `"`) > 2 {
		t.Errorf("Content-Disposition %q has a quote the sender broke out with", cd)
	}
}
