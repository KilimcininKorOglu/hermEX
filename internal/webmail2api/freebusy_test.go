package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	defer st.Close()
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
	st.Close()

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
