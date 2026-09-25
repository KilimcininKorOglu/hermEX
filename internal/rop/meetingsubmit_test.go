package rop

import (
	"bytes"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// namedTagsIn allocates named properties in a mailbox the way a MAPI client does
// through RopGetPropertyIdsFromNames before setting them, and returns their tags.
func namedTagsIn(t *testing.T, dir string, names []mapi.PropertyName, types []mapi.PropType) []mapi.PropTag {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids, err := st.GetNamedPropIDs(true, names)
	if err != nil {
		t.Fatal(err)
	}
	tags := make([]mapi.PropTag, len(ids))
	for i, id := range ids {
		tags[i] = mapi.PropTag(uint32(id)<<16 | uint32(types[i]))
	}
	return tags
}

// TestSubmitCounterProposalCarriesITIP submits a counter proposal the way Outlook
// stores one and checks the organizer receives it as an iTIP COUNTER carrying the
// proposed time. It used to leave as a plain mail with no calendar part, so a
// recipient outside the server saw neither the response nor the time proposed.
func TestSubmitCounterProposalCarriesITIP(t *testing.T) {
	ownerDir, aliceDir := t.TempDir(), t.TempDir()
	accounts := directory.StaticAccounts{
		"owner@hermex.test": {MailboxPath: ownerDir},
		"alice@hermex.test": {MailboxPath: aliceDir},
	}
	tags := namedTagsIn(t, ownerDir,
		[]mapi.PropertyName{mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole, mapi.NameAppointmentCounterProposal,
			mapi.NameAppointmentProposedStartWhole, mapi.NameAppointmentProposedEndWhole},
		[]mapi.PropType{mapi.PtSysTime, mapi.PtSysTime, mapi.PtBoolean, mapi.PtSysTime, mapi.PtSysTime})
	at := func(h int) uint64 { return mapi.UnixToNTTime(time.Date(2026, 7, 1, h, 0, 0, 0, time.UTC)) }

	sess := NewSession(ownerDir, accounts, "owner@hermex.test")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildCreateMessage(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDDraft))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Schedule.Meeting.Resp.Tent"},
		{Tag: mapi.PrSubject, Value: "New Time Proposed: Review"},
		{Tag: tags[0], Value: at(14)},
		{Tag: tags[1], Value: at(15)},
		{Tag: tags[2], Value: true},
		{Tag: tags[3], Value: at(16)},
		{Tag: tags[4], Value: at(17)},
	}), []uint32{msgH})
	toRow := buildSMTPRecipientRow(0, mapi.RecipTo, "alice@hermex.test", "Alice")
	sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, toRow), []uint32{msgH})
	sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	sub, _ := sess.Dispatch(buildSubmitMessage(0), []uint32{msgH})
	ropOK(t, sub, ropSubmitMessage, "SubmitMessage")

	raw := firstInboxRaw(t, aliceDir)
	for _, want := range []string{"text/calendar; charset=utf-8; method=COUNTER", "METHOD:COUNTER",
		"DTSTART:20260701T160000Z", "ATTENDEE;PARTSTAT=TENTATIVE:mailto:owner@hermex.test",
		"ORGANIZER;CN=\"Alice\":mailto:alice@hermex.test"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("delivered counter proposal missing %q:\n%s", want, raw)
		}
	}
}

// TestMeetingCalendarLeavesPlainMailAlone attaches nothing to a message that is not
// a meeting, so an ordinary submit is unchanged.
func TestMeetingCalendarLeavesPlainMailAlone(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	opt, err := meetingCalendar(st, &oxcmail.Message{Props: mapi.PropertyValues{{Tag: mapi.PrMessageClass, Value: "IPM.Note"}}})
	if err != nil || opt.CalendarBody != nil || opt.CalendarMethod != "" {
		t.Errorf("plain mail got calendar options %+v, err %v", opt, err)
	}
}
