package activesync

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// invitationAppData renders the seeded invitation's Sync ApplicationData at the given
// protocol version.
func invitationAppData(t *testing.T, protocol string) *wbxml.Node {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	uid := seedMeetingRequest(t, dir)
	info, err := st.MessageByUID(int64(mapi.PrivateFIDInbox), uid)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), uid)
	if err != nil {
		t.Fatal(err)
	}
	rc := mailRender{st: st, pref: bodyPref{typ: bodyTypePlain}, protocol: protocol}
	return emailAppData(rc, raw, info, "1", "5")
}

// TestInvitationCarriesTheMeetingRequestBlock is the load-bearing case: a delivered
// invitation must reach the device as the scheduling class WITH the MS-ASEMAIL
// MeetingRequest block. Without either, the device renders it as ordinary mail and
// never offers Accept / Tentative / Decline.
func TestInvitationCarriesTheMeetingRequestBlock(t *testing.T) {
	data := invitationAppData(t, "14.1")

	if cls := data.ChildText(wbxml.EMMessageClass); cls != meetingRequestClass {
		t.Errorf("message class = %q, want %q", cls, meetingRequestClass)
	}
	mr := data.Child(wbxml.EMMeetingRequest)
	if mr == nil {
		t.Fatal("no MeetingRequest block on a delivered invitation")
	}
	// DtStamp, TimeZone and the identifier are the required children; the span is
	// what the device shows.
	for _, c := range []struct {
		tag  wbxml.Tag
		want string
		name string
	}{
		{wbxml.EMStartTime, "20260701T140000Z", "StartTime"},
		{wbxml.EMEndTime, "20260701T150000Z", "EndTime"},
		{wbxml.EMInstanceType, meetingInstanceSingle, "InstanceType"},
		{wbxml.EMAllDayEvent, "0", "AllDayEvent"},
		{wbxml.EMResponseRequested, "1", "ResponseRequested"},
		{wbxml.EMOrganizer, "organizer@external.test", "Organizer"},
		{wbxml.EMTimeZone, utcTimezone, "TimeZone"},
		{wbxml.EM2MeetingMessageType, meetingMsgInitial, "MeetingMessageType"},
	} {
		if got := mr.ChildText(c.tag); got != c.want {
			t.Errorf("%s = %q, want %q", c.name, got, c.want)
		}
	}
	if mr.ChildText(wbxml.EMDtStamp) == "" {
		t.Error("DtStamp is a required child of MeetingRequest")
	}
}

// TestOrdinaryMailCarriesNoMeetingRequest is the negative control: the block is only
// for an invitation, and an ordinary mail keeps IPM.Note.
func TestOrdinaryMailCarriesNoMeetingRequest(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(convMsgRoot), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), info.UID)
	if err != nil {
		t.Fatal(err)
	}
	data := emailAppData(mailRender{st: st, protocol: "14.1"}, raw, info, "1", "5")

	if cls := data.ChildText(wbxml.EMMessageClass); cls != messageClassNote {
		t.Errorf("message class = %q, want %q", cls, messageClassNote)
	}
	if data.Child(wbxml.EMMeetingRequest) != nil {
		t.Error("ordinary mail must carry no MeetingRequest block")
	}
}

// TestMeetingIdentifierFollowsTheProtocol proves the version split: 14.x carries
// email:GlobalObjId and 16.x carries calendar:UID, the element that replaced it.
func TestMeetingIdentifierFollowsTheProtocol(t *testing.T) {
	old := invitationAppData(t, "14.1").Child(wbxml.EMMeetingRequest)
	if old.ChildText(wbxml.EMGlobalObjId) == "" {
		t.Error("14.1 must carry GlobalObjId")
	}
	if old.Child(wbxml.CalUID) != nil {
		t.Error("14.1 must not carry calendar:UID")
	}

	recent := invitationAppData(t, "16.1").Child(wbxml.EMMeetingRequest)
	if got := recent.ChildText(wbxml.CalUID); got != "eas-meeting-1" {
		t.Errorf("16.1 calendar:UID = %q, want the invitation's UID", got)
	}
	if recent.Child(wbxml.EMGlobalObjId) != nil {
		t.Error("16.1 must not carry GlobalObjId")
	}
}

// TestGlobalObjIDCarriesTheUID proves the identifier decodes back to the iCalendar
// UID through the documented steps: base64, the fixed class id, then the vCal marker
// and version ahead of the UID. A device that cannot recover the UID cannot match the
// invitation against a calendar object it already holds.
func TestGlobalObjIDCarriesTheUID(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(globalObjID("eas-meeting-1"))
	if err != nil {
		t.Fatalf("GlobalObjId is not base64: %v", err)
	}
	if len(raw) < 40 {
		t.Fatalf("GlobalObjId is %d bytes, shorter than its fixed header", len(raw))
	}
	if string(raw[:16]) != string(globalObjIDClassID) {
		t.Errorf("class id = %x, want %x", raw[:16], globalObjIDClassID)
	}
	data := string(raw[40:])
	if !strings.HasPrefix(data, "vCal-Uid") {
		t.Fatalf("data does not start with the vCal marker: %q", data)
	}
	uid := strings.TrimSuffix(data[len("vCal-Uid")+4:], "\x00")
	if uid != "eas-meeting-1" {
		t.Errorf("decoded UID = %q, want eas-meeting-1", uid)
	}
	if globalObjID("") != "" {
		t.Error("an absent UID must yield no identifier")
	}
}

// TestMeetingMessageTypeFollowsTheSequence pins the rule: the first invitation is an
// initial request, and a later revision (a higher iCalendar SEQUENCE) is a full update.
func TestMeetingMessageTypeFollowsTheSequence(t *testing.T) {
	if got := meetingMessageType(0); got != meetingMsgInitial {
		t.Errorf("SEQUENCE 0 = %q, want %q", got, meetingMsgInitial)
	}
	if got := meetingMessageType(2); got != meetingMsgFullUpdate {
		t.Errorf("SEQUENCE 2 = %q, want %q", got, meetingMsgFullUpdate)
	}
}
