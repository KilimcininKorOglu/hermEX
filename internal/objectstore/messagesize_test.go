package objectstore

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// storedSize reads a message's message_size column.
func storedSize(t *testing.T, st *Store, id int64) int64 {
	t.Helper()
	var n int64
	if err := st.objdb.QueryRow(`SELECT message_size FROM messages WHERE message_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestMessageSizeFollowsAnEdit proves an edit that grows a message grows the size
// its folder and the quota count ([MS-OXCFOLD] 2.2.2.2.1.11 sums it), instead of
// keeping the size the message was created with.
func TestMessageSizeFollowsAnEdit(t *testing.T) {
	st := openSeededStore(t)
	id, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note"}, {Tag: mapi.PrBody, Value: "short"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	before := storedSize(t, st, id)

	big := string(bytes.Repeat([]byte("x"), 4096))
	if err := st.SetMessageProperties(id, mapi.PropertyValues{{Tag: mapi.PrBody, Value: big}}); err != nil {
		t.Fatal(err)
	}
	if got := storedSize(t, st, id); got < before+4000 {
		t.Errorf("size after a 4 KB body = %d, was %d before the edit", got, before)
	}
	grown := storedSize(t, st, id)

	if _, _, err := st.CreateAttachment(id, mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachDataBin, Value: bytes.Repeat([]byte("y"), 8192)},
	}); err != nil {
		t.Fatal(err)
	}
	if got := storedSize(t, st, id); got < grown+8000 {
		t.Errorf("size after an 8 KB attachment = %d, was %d before it", got, grown)
	}
}
