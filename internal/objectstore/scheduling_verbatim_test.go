package objectstore

import (
	"bytes"
	"os"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestSchedulingMessageServedVerbatim proves a delivered meeting request is served
// byte-for-byte — its text/calendar invitation stays a body alternative rather than
// being demoted to an attachment by re-export — both from the eml cache and, with
// the cache gone, regenerated from the preserved original.
func TestSchedulingMessageServedVerbatim(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:verbatim-1\r\nSUMMARY:Review\r\n" +
		"DTSTART:20260701T140000Z\r\nDTEND:20260701T150000Z\r\n" +
		"ORGANIZER:mailto:org@external.test\r\nATTENDEE:mailto:alice@hermex.test\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	raw := "From: org@external.test\r\nTo: alice@hermex.test\r\nSubject: Review\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nPlease attend.\r\n" +
		"--b\r\nContent-Type: text/calendar; method=REQUEST; charset=UTF-8\r\n\r\n" + ics +
		"--b--\r\n"

	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Primary: served verbatim from the eml cache.
	got, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(raw)) {
		t.Fatalf("scheduling message not served verbatim:\n got %q\nwant %q", got, raw)
	}
	if !bytes.Contains(got, []byte("Content-Type: text/calendar")) {
		t.Error("served message lost its text/calendar body")
	}

	// Fallback: with the eml cache gone, the preserved original regenerates it
	// verbatim rather than re-exporting (which would demote the invitation).
	if err := os.Remove(st.emlPath(midString(uint64(info.ID)))); err != nil {
		t.Fatal(err)
	}
	got2, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, []byte(raw)) {
		t.Error("scheduling message not regenerated verbatim from the preserved original")
	}
}
