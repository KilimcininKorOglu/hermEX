package webmail2api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestTaskEditKeepsTheSameObject edits a task another client has touched: the
// id, the foreign property and the attachment stay, the cleared due date goes,
// and nothing lands in the recoverable items.
func TestTaskEditKeepsTheSameObject(t *testing.T) {
	do, dir := apiHarness(t)
	created := okBody[taskJSON](t, "create", do(http.MethodPost, "/api/v1/tasks",
		`{"summary":"Write the report","due":"2026-10-01","priority":2}`))
	id, err := strconv.ParseInt(created.UID, 10, 64)
	mustNoErr(t, "parse created id", err)
	st := openMailbox(t, dir)
	mustNoErr(t, "set foreign property", st.SetMessageProperties(id, mapi.PropertyValues{{Tag: hobbies, Value: "chess"}}))
	_, _, err = st.CreateAttachment(id, mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachLongFilename, Value: "notes.txt"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("notes")},
	})
	mustNoErr(t, "attach", err)

	got := okBody[taskJSON](t, "edit", do(http.MethodPut, "/api/v1/tasks/"+created.UID, `{"summary":"Write the final report","priority":2}`))
	wantEq(t, "returned id", got.UID, created.UID)

	after, err := st.OpenMessage(id)
	mustNoErr(t, "open after", err)
	wantEq(t, "subject", propStr(after.Props, mapi.PrSubject), "Write the final report")
	wantEq(t, "foreign property", propStr(after.Props, hobbies), "chess")
	wantEq(t, "attachments", len(after.Attachments), 1)
	wantEq(t, "due date", after.Props.Has(namedTag(t, st, mapi.NameTaskDueDate, mapi.PtSysTime)), false)
	wantEq(t, "common end", after.Props.Has(namedTag(t, st, mapi.NameCommonEnd, mapi.PtSysTime)), false)
	deleted, err := st.ListAllSoftDeleted()
	mustNoErr(t, "list recoverable", err)
	wantEq(t, "recoverable items", len(deleted), 0)
	wantEq(t, "listed task", listOneTask(t, do, "list").UID, created.UID)
}

// TestTaskEditRefusesWhatIsNotATask answers 400 for a malformed id and 404 for an
// unknown id or a mail message, and leaves the message as it was.
func TestTaskEditRefusesWhatIsNotATask(t *testing.T) {
	do, dir := apiHarness(t)
	st := openMailbox(t, dir)
	info, err := st.AppendMessage(mapi.PrivateFIDInbox,
		[]byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: mail\r\n\r\nbody\r\n"), time.Now(), 0)
	mustNoErr(t, "append", err)

	wantStatus(t, "malformed id", do(http.MethodPut, "/api/v1/tasks/x", `{"summary":"x"}`), http.StatusBadRequest)
	for _, id := range []int64{info.ID, 1 << 40} {
		wantStatus(t, "edit", do(http.MethodPut, "/api/v1/tasks/"+strconv.FormatInt(id, 10), `{"summary":"x"}`), http.StatusNotFound)
	}
	msg, err := st.OpenMessage(info.ID)
	mustNoErr(t, "open mail", err)
	wantEq(t, "mail subject", propStr(msg.Props, mapi.PrSubject), "mail")
}
