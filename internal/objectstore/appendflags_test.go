package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// objReadState reads a message's object-store read state and read change number.
func objReadState(t *testing.T, st *Store, id int64) (read int, hasCN bool) {
	t.Helper()
	var cn *int64
	mustScan(t, st.objdb.QueryRow(`SELECT read_state, read_cn FROM messages WHERE message_id=?`, id), &read, &cn)
	return read, cn != nil
}

// TestAppendSeenIsReadEverywhere proves a message appended with \Seen is read in
// the object store, which MAPI, EWS and ActiveSync read, as it is in the index.
func TestAppendSeenIsReadEverywhere(t *testing.T) {
	st := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	seen := mustAppendMessage(t, st, inbox, []byte("From: a@hermex.test\r\nSubject: seen\r\n\r\nx\r\n"), time.Now(), FlagSeen)
	unseen := mustAppendMessage(t, st, inbox, []byte("From: a@hermex.test\r\nSubject: unseen\r\n\r\nx\r\n"), time.Now(), 0)
	if read, _ := objReadState(t, st, seen.ID); read != 1 {
		t.Errorf("appended \\Seen message read_state = %d, want 1", read)
	}
	if read, _ := objReadState(t, st, unseen.ID); read != 0 {
		t.Errorf("appended unseen message read_state = %d, want 0", read)
	}
}

// storedFlags reads a message's stored PidTagMessageFlags.
func storedFlags(t *testing.T, st *Store, id int64) int32 {
	t.Helper()
	props, err := st.GetMessageProperties(id, mapi.PrMessageFlags)
	mustNoErr(t, "read flags", err)
	v, _ := props.Get(mapi.PrMessageFlags)
	f, _ := v.(int32)
	return f
}

// TestAppendDraftIsUnsent proves a message appended with \Draft is unsent to a
// MAPI client, which then treats it as a draft it may edit and send.
func TestAppendDraftIsUnsent(t *testing.T) {
	st := openSeededStore(t)
	draft := mustAppendMessage(t, st, int64(mapi.PrivateFIDDraft), []byte("From: a@hermex.test\r\nSubject: draft\r\n\r\nx\r\n"), time.Now(), FlagDraft)
	sent := mustAppendMessage(t, st, int64(mapi.PrivateFIDSentItems), []byte("From: a@hermex.test\r\nSubject: sent\r\n\r\nx\r\n"), time.Now(), FlagSeen)
	if f := storedFlags(t, st, draft.ID); f&mapi.MsgFlagUnsent == 0 {
		t.Errorf("appended \\Draft flags = %#x, want the unsent bit", f)
	}
	if f := storedFlags(t, st, sent.ID); f&mapi.MsgFlagUnsent != 0 {
		t.Errorf("appended sent copy flags = %#x, want no unsent bit", f)
	}
}

// TestRepairAppendedDrafts proves the next open marks unsent a draft an earlier
// append stored as sent, advancing its change number.
func TestRepairAppendedDrafts(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	mustNoErr(t, "open", err)
	draft := mustAppendMessage(t, st, int64(mapi.PrivateFIDDraft), []byte("From: a@hermex.test\r\nSubject: draft\r\n\r\nx\r\n"), time.Now(), FlagDraft)
	_, err = st.objdb.Exec(`DELETE FROM message_properties WHERE message_id=? AND proptag=?`, draft.ID, int64(uint32(mapi.PrMessageFlags)))
	mustNoErr(t, "drop the flags", err)
	var before int64
	mustScan(t, st.objdb.QueryRow(`SELECT change_number FROM messages WHERE message_id=?`, draft.ID), &before)
	_, err = st.objdb.Exec(`DELETE FROM configurations WHERE config_id=?`, cfgDraftRepaired)
	mustNoErr(t, "clear the marker", err)
	mustNoErr(t, "close", st.Close())

	st, err = Open(dir)
	mustNoErr(t, "reopen", err)
	defer st.Close()
	if f := storedFlags(t, st, draft.ID); f&mapi.MsgFlagUnsent == 0 {
		t.Errorf("repaired draft flags = %#x, want the unsent bit", f)
	}
	var after int64
	mustScan(t, st.objdb.QueryRow(`SELECT change_number FROM messages WHERE message_id=?`, draft.ID), &after)
	if after <= before {
		t.Errorf("change number %d did not advance past %d", after, before)
	}
}

// TestRepairAppendedReadState proves the next open marks read a message an
// earlier append left seen in the index and unread in the object store, with a
// read change number a synchronized client downloads, and leaves alone a message
// whose read state a client set.
func TestRepairAppendedReadState(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	mustNoErr(t, "open", err)
	inbox := int64(mapi.PrivateFIDInbox)
	stale := mustAppendMessage(t, st, inbox, []byte("From: a@hermex.test\r\nSubject: stale\r\n\r\nx\r\n"), time.Now(), FlagSeen)
	chosen := mustAppendMessage(t, st, inbox, []byte("From: a@hermex.test\r\nSubject: chosen\r\n\r\nx\r\n"), time.Now(), FlagSeen)
	// The state an append left before the fix.
	_, err = st.objdb.Exec(`UPDATE messages SET read_state=0 WHERE message_id=?`, stale.ID)
	mustNoErr(t, "unmark", err)
	// A client marked this one unread in the object store only.
	_, err = st.objdb.Exec(`UPDATE messages SET read_state=0, read_cn=999999 WHERE message_id=?`, chosen.ID)
	mustNoErr(t, "client unread", err)
	_, err = st.objdb.Exec(`DELETE FROM configurations WHERE config_id=?`, cfgReadStateRepaired)
	mustNoErr(t, "clear the marker", err)
	mustNoErr(t, "close", st.Close())

	st, err = Open(dir)
	mustNoErr(t, "reopen", err)
	defer st.Close()
	if read, hasCN := objReadState(t, st, stale.ID); read != 1 || !hasCN {
		t.Errorf("repaired message read_state = %d with read change %v, want 1 with one", read, hasCN)
	}
	if read, _ := objReadState(t, st, chosen.ID); read != 0 {
		t.Errorf("a client-set read state was overwritten: read_state = %d", read)
	}
	var done int64
	mustScan(t, st.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgReadStateRepaired), &done)
	wantEq(t, "the repair marker", done, int64(1))
}
