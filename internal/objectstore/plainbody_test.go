package objectstore

import (
	"bytes"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// htmlOnlyMail is a message whose only body is HTML.
var htmlOnlyMail = []byte("From: a@hermex.test\r\nSubject: invoice\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n\r\n" +
	"<html><body><p>Your invoice <b>total</b> is due</p></body></html>\r\n")

// TestHTMLOnlyMailMatchesABodyCondition proves an inbox rule or a search folder
// testing the body text matches a message whose only body is HTML.
func TestHTMLOnlyMailMatchesABodyCondition(t *testing.T) {
	st := openSeededStore(t)
	info := mustAppendMessage(t, st, int64(mapi.PrivateFIDInbox), htmlOnlyMail, time.Now(), 0)
	props, err := st.GetMessageProperties(info.ID)
	mustNoErr(t, "read the message", err)
	if !evalRestriction(RuleBodyContains("invoice total"), props) {
		t.Error("a body condition does not match the text of an HTML-only message")
	}
}

// TestRepairPlainBodies proves the next open gives a message stored before the
// import derived its plain body that body, without a change number and without
// changing the bytes an IMAP client already read.
func TestRepairPlainBodies(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	mustNoErr(t, "open", err)
	inbox := int64(mapi.PrivateFIDInbox)
	info := mustAppendMessage(t, st, inbox, htmlOnlyMail, time.Now(), 0)
	_, err = st.objdb.Exec(`DELETE FROM message_properties WHERE message_id=? AND proptag=?`, info.ID, int64(uint32(mapi.PrBody)))
	mustNoErr(t, "drop the plain body", err)
	_, err = st.objdb.Exec(`DELETE FROM configurations WHERE config_id=?`, cfgPlainBodyRepaired)
	mustNoErr(t, "clear the marker", err)
	var before int64
	mustScan(t, st.objdb.QueryRow(`SELECT change_number FROM messages WHERE message_id=?`, info.ID), &before)
	served, err := st.GetMessageRaw(inbox, info.UID)
	mustNoErr(t, "read the served bytes", err)
	mustNoErr(t, "close", st.Close())

	st, err = Open(dir)
	mustNoErr(t, "reopen", err)
	defer st.Close()
	props, err := st.GetMessageProperties(info.ID, mapi.PrBody)
	mustNoErr(t, "read the body", err)
	if body, _ := props.Get(mapi.PrBody); body != "Your invoice total is due" {
		t.Errorf("repaired PR_BODY = %q, want the text of the HTML", body)
	}
	var after int64
	mustScan(t, st.objdb.QueryRow(`SELECT change_number FROM messages WHERE message_id=?`, info.ID), &after)
	wantEq(t, "the change number", after, before)
	again, err := st.GetMessageRaw(inbox, info.UID)
	mustNoErr(t, "reread the served bytes", err)
	if !bytes.Equal(served, again) {
		t.Error("the repair changed the bytes an IMAP client already read")
	}
}
