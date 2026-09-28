package objectstore

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestInternetHeaderNamesStopAtTheQuota proves a store stops naming
// PS_INTERNET_HEADERS fields at its quota, so invented header names cannot spend
// the id space every other named property needs, while other namespaces still
// allocate and a name already held still resolves.
func TestInternetHeaderNamesStopAtTheQuota(t *testing.T) {
	s := openTestStore(t)
	held := mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsInternetHeaders, Name: "X-Held"}
	ids, err := s.GetNamedPropIDs(true, []mapi.PropertyName{held})
	if err != nil {
		t.Fatal(err)
	}
	filler := make([]mapi.PropertyName, internetHeaderNameQuota-1)
	for i := range filler {
		filler[i] = mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsInternetHeaders, Name: "X-Fill-" + strconv.Itoa(i)}
	}
	if _, err := s.GetNamedPropIDs(true, filler); err != nil {
		t.Fatal(err)
	}

	over := mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsInternetHeaders, Name: "X-Over"}
	other := mapi.PropertyName{Kind: mapi.MnidString, GUID: npGUID, Name: "other-namespace"}
	got, err := s.GetNamedPropIDs(true, []mapi.PropertyName{over, other, held})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 0 {
		t.Errorf("a name past the quota was allocated id %#x", got[0])
	}
	if got[1] == 0 {
		t.Error("another namespace must still allocate at the quota")
	}
	if got[2] != ids[0] {
		t.Errorf("a held name resolved to %#x, want %#x", got[2], ids[0])
	}
}

// TestDeliveredHeaderFieldSurvivesRegeneration proves the whole path: a received
// field no property carries is stored as a PS_INTERNET_HEADERS property, and a
// message rendered from the store again carries it.
func TestDeliveredHeaderFieldSurvivesRegeneration(t *testing.T) {
	s := openSeededStore(t)
	raw := "From: a@example.org\r\nTo: b@example.org\r\nSubject: s\r\nX-Ticket: 42\r\n\r\nbody\r\n"
	info, err := s.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1718200000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.OpenMessage(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := oxcmail.Export(msg, s.ExportOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "X-Ticket: 42\r\n") {
		t.Errorf("the received field did not survive a store round trip:\n%s", out)
	}
}
