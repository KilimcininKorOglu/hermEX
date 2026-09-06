package webmail2api

import (
	"net/http"
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
	st.Close()

	do := apiHarnessFor(t, dir)
	type counts struct{ Affected, Evaluated int }
	resp := okBody[counts](t, "run", do(http.MethodPost, "/api/v1/filters/run", ""))
	wantEq(t, "evaluated", resp.Evaluated, 2)
	wantEq(t, "affected (only the promo message matched)", resp.Affected, 1)

	// The non-matching message must stay in the Inbox: a mark-read rule never
	// removes mail, and run-now must not touch messages no rule matched.
	st2, err := objectstore.Open(dir)
	mustNoErr(t, "reopen mailbox", err)
	defer st2.Close()
	msgs, err := st2.ListMessages(inbox)
	mustNoErr(t, "list messages", err)
	wantEq(t, "inbox messages after the run (mark-read keeps both)", len(msgs), 2)
}
