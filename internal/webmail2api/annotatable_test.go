package webmail2api

import (
	"net/http"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// annotatableMails is a mailbox holding one mail with a Message-ID and one
// without.
type annotatableMails struct {
	mbox              string
	withID, withoutID uint32
}

// annotatableMailbox stores the two mails annotatableMails describes.
func annotatableMailbox(t *testing.T) annotatableMails {
	t.Helper()
	mbox := freshMailbox(t)
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	defer mustClose(t, st)
	withID, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: bob@hermex.test\r\nMessage-ID: <n1@hermex.test>\r\nSubject: linked\r\n\r\nhi\r\n"), time.Now(), 0)
	mustNoErr(t, "store the linked mail", err)
	withoutID, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: bob@hermex.test\r\nSubject: bare\r\n\r\nhi\r\n"), time.Now(), 0)
	mustNoErr(t, "store the bare mail", err)
	return annotatableMails{mbox: mbox, withID: withID.UID, withoutID: withoutID.UID}
}

// TestMailDetailReportsWhetherANoteCanBeAdded proves the reader learns whether a
// mail can carry a note. A note links to its mail by the Message-ID, so a mail
// without one refuses the note; the reader offered the action anyway and the
// user met a generic error.
func TestMailDetailReportsWhetherANoteCanBeAdded(t *testing.T) {
	m := annotatableMailbox(t)
	mbox, withID, withoutID := m.mbox, m.withID, m.withoutID
	do := apiHarnessFor(t, mbox)
	annotatable := func(uid uint32) bool {
		d := okBody[struct {
			Annotatable bool `json:"annotatable"`
		}](t, "GET message", do(http.MethodGet, "/api/v1/mail/message?id="+messageID("inbox", uid), ""))
		return d.Annotatable
	}

	if !annotatable(withID) {
		t.Error("a mail with a Message-ID reads as not annotatable")
	}
	if annotatable(withoutID) {
		t.Error("a mail without a Message-ID reads as annotatable, so the reader offers a note the server refuses")
	}
	wantStatus(t, "GET a uid that names no mail", do(http.MethodGet, "/api/v1/mail/message?id="+messageID("inbox", withoutID+100), ""), http.StatusNotFound)
}

// TestANoteOnAMailWithoutMessageIDIsRefused proves the server keeps the promise
// the detail makes: a note on a mail with no Message-ID, or on a mail that does
// not exist, is refused and the Notes folder stays empty.
func TestANoteOnAMailWithoutMessageIDIsRefused(t *testing.T) {
	m := annotatableMailbox(t)
	mbox, withoutID := m.mbox, m.withoutID
	do := apiHarnessFor(t, mbox)
	wantStatus(t, "note on a mail without a Message-ID",
		do(http.MethodPost, "/api/v1/mail/notes", `{"id":"`+messageID("inbox", withoutID)+`","title":"x","body":"y"}`), http.StatusBadRequest)
	wantStatus(t, "note on a missing mail",
		do(http.MethodPost, "/api/v1/mail/notes", `{"id":"`+messageID("inbox", withoutID+100)+`","title":"x","body":"y"}`), http.StatusNotFound)

	st, err := objectstore.OpenExisting(mbox)
	mustNoErr(t, "reopen the mailbox", err)
	defer mustClose(t, st)
	notes, err := st.ListFolderObjects(int64(mapi.PrivateFIDNotes))
	mustNoErr(t, "list the notes", err)
	wantEq(t, "notes after the refusals", len(notes), 0)
}
