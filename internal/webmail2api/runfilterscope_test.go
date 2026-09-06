package webmail2api

import (
	"net/http"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestRunFiltersScope covers the two selections the endpoint accepts. A user who
// writes a rule after the fact needs to run it over the folder the old mail is in,
// and a user who fixes one rule needs that rule applied without the others.
func TestRunFiltersScope(t *testing.T) {
	dir := t.TempDir()
	junk := int64(mapi.PrivateFIDJunk)
	junkMsg := seedPromoRule(t, dir)
	do := apiHarnessFor(t, dir)
	runScoped := func(what, body string) struct{ Affected, Evaluated int } {
		t.Helper()
		type counts struct{ Affected, Evaluated int }
		return okBody[counts](t, what, do(http.MethodPost, "/api/v1/filters/run", body))
	}

	t.Run("named folder", func(t *testing.T) {
		out := runScoped("named folder", `{"folder":"junk"}`)
		wantEq(t, "evaluated from Junk", out.Evaluated, 1)
		wantEq(t, "affected from Junk", out.Affected, 1)
		st, err := objectstore.Open(dir)
		mustNoErr(t, "reopen mailbox", err)
		defer st.Close()
		if fl, _ := st.MessageFlags(junk, junkMsg.UID); fl&objectstore.FlagSeen == 0 {
			t.Error("the Junk message was not touched, so the folder selection was ignored")
		}
	})

	t.Run("unknown folder", func(t *testing.T) {
		wantStatus(t, "unknown folder", do(http.MethodPost, "/api/v1/filters/run", `{"folder":"nowhere"}`), http.StatusNotFound)
	})

	t.Run("unknown filter", func(t *testing.T) {
		// A stale selection must not fall back to running everything.
		wantStatus(t, "unknown filter", do(http.MethodPost, "/api/v1/filters/run", `{"filter":"no-such-filter"}`), http.StatusNotFound)
	})

	t.Run("empty body still runs the inbox", func(t *testing.T) {
		wantEq(t, "evaluated (the Inbox's one message)", runScoped("empty body", "").Evaluated, 1)
	})
}

// seedPromoRule fills a mailbox with one promo message in the Inbox and one in
// Junk, plus the mark-read rule both match, and returns the Junk message's row.
func seedPromoRule(t *testing.T, dir string) objectstore.MessageInfo {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	defer st.Close()
	inbox := int64(mapi.PrivateFIDInbox)
	promo := []byte("From: news@promo.com\r\nSubject: Weekly promo\r\n\r\nbuy\r\n")
	_, err = st.AppendMessage(inbox, promo, time.Now(), 0)
	mustNoErr(t, "append inbox promo", err)
	junkMsg, err := st.AppendMessage(int64(mapi.PrivateFIDJunk), promo, time.Now(), 0)
	mustNoErr(t, "append junk promo", err)
	_, err = st.AddRule(objectstore.Rule{
		FolderID: inbox, Name: "read promos", Sequence: 0, State: mapi.RuleStateEnabled,
		Condition: objectstore.RuleSubjectContains("promo"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleMarkReadAction()}},
	})
	mustNoErr(t, "add rule", err)
	return junkMsg
}
