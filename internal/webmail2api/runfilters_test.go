package webmail2api

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestRunFiltersNow proves POST /filters/run ports the old webmail's "run now":
// it applies the Inbox's stored rules to the mail already sitting in the Inbox.
// Two messages are seeded; a mark-read rule matches only one, so the handler
// must report 2 evaluated and 1 affected, and run-now must not move or delete
// the non-matching message.
func TestRunFiltersNow(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	inbox := int64(mapi.PrivateFIDInbox)
	_, err = st.AppendMessage(inbox, []byte("From: news@promo.com\r\nSubject: Weekly promo\r\n\r\nbuy\r\n"), time.Now(), 0)
	mustNoErr(t, "append promo", err)
	_, err = st.AppendMessage(inbox, []byte("From: bob@b.test\r\nSubject: lunch\r\n\r\nhi\r\n"), time.Now(), 0)
	mustNoErr(t, "append keep", err)
	_, err = st.AddRule(objectstore.Rule{
		FolderID: inbox, Name: "read promos", State: mapi.RuleStateEnabled,
		Condition: objectstore.RuleSubjectContains("promo"),
		Actions:   mapi.RuleActions{Blocks: []mapi.ActionBlock{objectstore.RuleMarkReadAction()}},
	})
	mustNoErr(t, "add rule", err)
	mustClose(t, st)

	do := apiHarnessFor(t, dir)
	wantStatus(t, "unknown folder", do(http.MethodPost, "/api/v1/filters/run", `{"folder":"../../etc"}`), http.StatusNotFound)
	wantStatus(t, "malformed body", do(http.MethodPost, "/api/v1/filters/run", `{`), http.StatusBadRequest)
	type counts struct{ Affected, Evaluated int }
	resp := okBody[counts](t, "run", do(http.MethodPost, "/api/v1/filters/run", ""))
	wantEq(t, "evaluated", resp.Evaluated, 2)
	wantEq(t, "affected (only the promo message matched)", resp.Affected, 1)

	// The non-matching message must stay in the Inbox: a mark-read rule never
	// removes mail, and run-now must not touch messages no rule matched.
	st2, err := objectstore.Open(dir)
	mustNoErr(t, "reopen mailbox", err)
	defer mustClose(t, st2)
	msgs, err := st2.ListMessages(inbox)
	mustNoErr(t, "list messages", err)
	wantEq(t, "inbox messages after the run (mark-read keeps both)", len(msgs), 2)
	for _, m := range msgs {
		read := m.Flags&objectstore.FlagSeen != 0
		if want := m.Subject == "Weekly promo"; read != want {
			t.Errorf("%q read = %v, want %v", m.Subject, read, want)
		}
	}
}

// TestRunFiltersOnADamagedStore: when the rules cannot be read, the run
// answers 500 and changes no message.
func TestRunFiltersOnADamagedStore(t *testing.T) {
	dir := freshMailbox(t)
	st := openMailbox(t, dir)
	inbox := int64(mapi.PrivateFIDInbox)
	_, err := st.AppendMessage(inbox, []byte("From: a@b.test\r\nSubject: promo\r\n\r\nx\r\n"), time.Now(), 0)
	mustNoErr(t, "append", err)
	db, err := sql.Open("sqlite", filepath.Join(dir, "objects.sqlite3"))
	mustNoErr(t, "open objects db", err)
	_, err = db.Exec(`DROP TABLE rules`)
	mustNoErr(t, "drop rules", err)
	mustNoErr(t, "close objects db", db.Close())

	rec := apiHarnessFor(t, dir)(http.MethodPost, "/api/v1/filters/run", "")
	wantStatus(t, "run on a damaged store", rec, http.StatusInternalServerError)
	msgs, err := st.ListMessages(inbox)
	mustNoErr(t, "list", err)
	if len(msgs) != 1 || msgs[0].Flags&objectstore.FlagSeen != 0 {
		t.Errorf("inbox after the failed run = %+v, want the one unread message", msgs)
	}
}
