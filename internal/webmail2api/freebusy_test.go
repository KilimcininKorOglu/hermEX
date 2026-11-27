package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// seedBusyBlock writes one appointment with the given busy status into the
// mailbox's calendar.
func seedBusyBlock(t *testing.T, mbox string, start, end time.Time, status int32) {
	t.Helper()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole, mapi.NameBusyStatus,
	})
	if err != nil {
		t.Fatal(err)
	}
	tag := func(i int, typ mapi.PropType) mapi.PropTag {
		return mapi.PropTag(uint32(ids[i])<<16 | uint32(typ))
	}
	props := mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
		{Tag: mapi.PrSubject, Value: "block"},
		{Tag: tag(0, mapi.PtSysTime), Value: mapi.UnixToNTTime(start)},
		{Tag: tag(1, mapi.PtSysTime), Value: mapi.UnixToNTTime(end)},
		{Tag: tag(2, mapi.PtLong), Value: status},
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props}); err != nil {
		t.Fatal(err)
	}
}

// busyBlocks asks the free/busy endpoint for the caller's own day.
func busyBlocks(t *testing.T, status int32) int {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)

	day := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	seedBusyBlock(t, dir, day, day.Add(time.Hour), status)

	secret := []byte("freebusy-test-secret")
	accs := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: dir}}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{
		"users": {"alice@hermex.test"},
		"start": {day.Add(-time.Hour).Format(time.RFC3339)},
		"end":   {day.Add(8 * time.Hour).Format(time.RFC3339)},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/calendar/freebusy?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("freebusy = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		FreeBusy []struct {
			User string `json:"user"`
			Busy []struct {
				Start string `json:"start"`
			} `json:"busy"`
		} `json:"freeBusy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.FreeBusy) != 1 {
		t.Fatalf("got %d entries, want 1", len(out.FreeBusy))
	}
	return len(out.FreeBusy[0].Busy)
}

// TestFreeBusyRefusesAWindowOverTheLimit proves the free/busy endpoint serves
// at most the 42-day window the EWS ErrorTimeIntervalTooBig reference names,
// the limit EWS GetUserAvailability and the EAS ResolveRecipients free/busy
// apply to the same calendar scan. A wider window, such as one spanning
// centuries, is refused with 400 rather than scanning every calendar.
func TestFreeBusyRefusesAWindowOverTheLimit(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)
	secret := []byte("freebusy-test-secret")
	accs := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: dir}}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		end  time.Time
		want int
	}{
		{start.Add(42 * 24 * time.Hour), http.StatusOK},
		{start.Add(42*24*time.Hour + time.Second), http.StatusBadRequest},
		{time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), http.StatusBadRequest},
		{start.Add(-time.Hour), http.StatusBadRequest},
	} {
		q := url.Values{"users": {"alice@hermex.test"}, "start": {start.Format(time.RFC3339)}, "end": {c.end.Format(time.RFC3339)}}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/calendar/freebusy?"+q.Encode(), nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("window to %s: status %d, want %d", c.end.Format(time.RFC3339), rec.Code, c.want)
		}
	}
}

// TestFreeBusyHidesACalendarWithoutAGrant: another user's free/busy needs a
// free/busy right on their calendar (MS-OXCPERM FreeBusySimple), as EWS and
// CalDAV require. A new calendar's Default member holds it (Exchange's
// "Free/Busy time" default), so bob's time shows; once bob sets Default to
// None, it does not. A start that is no time is refused.
func TestFreeBusyHidesACalendarWithoutAGrant(t *testing.T) {
	alice, bob := freshMailbox(t), freshMailbox(t)
	day := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	seedBusyBlock(t, bob, day, day.Add(time.Hour), mapi.BusyBusy)
	revoke := func() {
		mustNoErr(t, "set Default to None", openMailbox(t, bob).ModifyPermissions(int64(mapi.PrivateFIDCalendar), false,
			[]objectstore.PermissionChange{{Op: objectstore.PermModify, MemberID: 0, Rights: 0}}))
	}
	secret := []byte("freebusy-test-secret")
	accs := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: alice}, "bob@hermex.test": {MailboxPath: bob}}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)
	token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: alice, Exp: time.Now().Add(time.Hour).Unix()})
	get := func(q url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/calendar/freebusy?"+q.Encode(), nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	window := url.Values{"users": {"bob@hermex.test"}, "start": {day.Add(-time.Hour).Format(time.RFC3339)}, "end": {day.Add(8 * time.Hour).Format(time.RFC3339)}}
	rec := get(window)
	wantStatus(t, "freebusy of bob by default", rec, http.StatusOK)
	wantContains(t, "bob's default free/busy", rec.Body.String(), "2026-06-19T09:00")
	revoke()
	rec = get(window)
	wantStatus(t, "freebusy of bob", rec, http.StatusOK)
	if body := rec.Body.String(); strings.Contains(body, "2026-06-19T09:00") {
		t.Errorf("alice sees bob's busy time without a grant: %s", body)
	}
	wantStatus(t, "unreadable start", get(url.Values{"users": {"bob@hermex.test"}, "start": {"yesterday"}, "end": {day.Format(time.RFC3339)}}), http.StatusBadRequest)
}

// TestFreeBusyReportsOnlyOccupiedTime is what the grid means. An appointment
// marked free, and one marking a home office day, say where the attendee is
// rather than that they are unavailable, so reporting them as busy would empty
// the day of candidate times for whoever is trying to book them.
func TestFreeBusyReportsOnlyOccupiedTime(t *testing.T) {
	for _, c := range []struct {
		status int32
		name   string
		want   int
	}{
		{mapi.BusyFree, "free", 0},
		{mapi.BusyWorkingElsewhere, "working elsewhere", 0},
		{mapi.BusyTentative, "tentative", 1},
		{mapi.BusyBusy, "busy", 1},
		{mapi.BusyOOF, "out of office", 1},
	} {
		if got := busyBlocks(t, c.status); got != c.want {
			t.Errorf("%s reported %d busy blocks, want %d", c.name, got, c.want)
		}
	}
}
