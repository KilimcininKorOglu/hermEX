package objectstore

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestAppendDatesADatelessMessageByItsArrival proves a message without a Date
// header is dated by the internal date it was appended with, not by the time the
// append ran. The append time used to become the message's Date, so a message
// appended for 2024 read as sent on the day it was stored.
func TestAppendDatesADatelessMessageByItsArrival(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	arrived := time.Date(2024, 6, 12, 13, 46, 40, 0, time.UTC)
	raw := []byte("From: ops@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Welcome\r\n\r\nready\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, arrived, 0)
	if err != nil {
		t.Fatal(err)
	}
	served, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Date: Wed, 12 Jun 2024 13:46:40 +0000"; !strings.Contains(string(served), want) {
		t.Errorf("served message lacks %q:\n%s", want, served)
	}
	props, err := st.GetMessageProperties(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := props.Get(mapi.PrClientSubmitTime); v != mapi.UnixToNTTime(arrived) {
		t.Errorf("submit time = %v, want the arrival %v", v, mapi.UnixToNTTime(arrived))
	}
}
