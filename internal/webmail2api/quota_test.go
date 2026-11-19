package webmail2api

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestAWriteIntoAFullMailboxIsRefused fills the mailbox past its receive quota,
// past which nothing new may enter it, whether by delivery or by the user. Every
// endpoint that adds an item must answer 507 Insufficient Storage (RFC 4918
// 11.5) and store nothing; editing what the mailbox holds stays open, and the
// previous draft survives a refused autosave.
func TestAWriteIntoAFullMailboxIsRefused(t *testing.T) {
	do, dir := apiHarness(t)
	draft := do(http.MethodPost, "/api/v1/mail/draft", `{"to":["bob@hermex.test"],"subject":"first","body":"x"}`)
	wantStatus(t, "first draft", draft, http.StatusOK)
	task := okBody[taskJSON](t, "create task", do(http.MethodPost, "/api/v1/tasks", `{"summary":"Keep"}`))

	st := openMailbox(t, dir)
	big := "From: a@example.test\r\nMessage-ID: <big@example.test>\r\nSubject: big\r\n\r\n" + strings.Repeat("x", 4096)
	_, err := st.AppendMessage(mapi.PrivateFIDInbox, []byte(big), time.Unix(3, 0), 0)
	mustNoErr(t, "fill", err)
	mustNoErr(t, "set quota", st.SetQuota(objectstore.QuotaLimits{ReceiveKB: 1}))
	before := countItems(t, st)

	eml := base64.StdEncoding.EncodeToString([]byte("From: a@example.test\r\nSubject: imp\r\n\r\nbody\r\n"))
	writes := map[string][2]string{
		"event":     {"/api/v1/calendar/events", berlinStandup},
		"contact":   {"/api/v1/contacts", `{"name":"Ada Lovelace"}`},
		"task":      {"/api/v1/tasks", `{"summary":"New"}`},
		"note":      {"/api/v1/notes", `{"title":"Idea","body":"x"}`},
		"mail note": {"/api/v1/mail/notes", `{"id":"inbox:1","title":"N","body":"x"}`},
		"copy":      {"/api/v1/mail/copy", `{"id":"inbox:1","to":"drafts"}`},
		"import":    {"/api/v1/mail/import", `{"file":"` + eml + `"}`},
		"draft":     {"/api/v1/mail/draft", `{"to":["bob@hermex.test"],"subject":"second","body":"y"}`},
	}
	for name, w := range writes {
		if rec := do(http.MethodPost, w[0], w[1]); rec.Code != http.StatusInsufficientStorage {
			t.Errorf("%s into a full mailbox: status %d body %s, want 507", name, rec.Code, rec.Body.String())
		}
	}
	if after := countItems(t, st); after != before {
		t.Errorf("refused writes changed the mailbox: %d items before, %d after", before, after)
	}
	wantStatus(t, "edit a task in a full mailbox", do(http.MethodPut, "/api/v1/tasks/"+task.UID, `{"summary":"Kept"}`), http.StatusOK)
}

// countItems counts every mail message and object-store item the test touches.
func countItems(t *testing.T, st *objectstore.Store) int {
	t.Helper()
	n := 0
	for _, fid := range []int64{mapi.PrivateFIDInbox, mapi.PrivateFIDDraft} {
		msgs, err := st.ListMessages(fid)
		mustNoErr(t, "list messages", err)
		n += len(msgs)
	}
	for _, fid := range []int64{mapi.PrivateFIDCalendar, mapi.PrivateFIDContacts, mapi.PrivateFIDTasks, mapi.PrivateFIDNotes} {
		objs, err := st.ListFolderObjects(fid)
		mustNoErr(t, "list objects", err)
		n += len(objs)
	}
	return n
}
