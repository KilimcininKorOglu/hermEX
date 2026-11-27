package activesync

import (
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

func settingsReq(subs ...*wbxml.Node) *wbxml.Node {
	return wbxml.Elem(wbxml.STSettings, subs...)
}

// oofMessageNode builds an OofMessage block for a Set request.
func oofMessageNode(appliesTo wbxml.Tag, enabled, reply string) *wbxml.Node {
	return wbxml.Elem(wbxml.STOofMessage,
		wbxml.Empty(appliesTo),
		wbxml.Str(wbxml.STEnabled, enabled),
		wbxml.Str(wbxml.STReplyMessage, reply),
		wbxml.Str(wbxml.STBodyType, "Text"))
}

// internalOof finds the AppliesToInternal OofMessage in an Oof Get body.
func oofMessageFor(get *wbxml.Node, appliesTo wbxml.Tag) *wbxml.Node {
	for _, m := range get.Children {
		if m.Tag == wbxml.STOofMessage && m.Child(appliesTo) != nil {
			return m
		}
	}
	return nil
}

// TestSettingsUserInformation confirms UserInformation Get returns the account's
// SMTP address in the 14.1 Accounts shape (the negotiated protocol version).
func TestSettingsUserInformation(t *testing.T) {
	ts, _ := seededServer(t)
	req := settingsReq(wbxml.Elem(wbxml.STUserInformation, wbxml.Empty(wbxml.STGet)))
	_, root := postCommand(t, ts, "Settings", req)

	if root.ChildText(wbxml.STStatus) != "1" {
		t.Errorf("Settings Status = %q, want 1", root.ChildText(wbxml.STStatus))
	}
	ui := root.Child(wbxml.STUserInformation)
	if ui == nil || ui.ChildText(wbxml.STStatus) != "1" {
		t.Fatalf("missing UserInformation/Status 1")
	}
	acct := ui.Child(wbxml.STGet).Child(wbxml.STAccounts).Child(wbxml.STAccount)
	if acct == nil {
		t.Fatal("14.1 response missing Accounts/Account")
	}
	if smtp := acct.Child(wbxml.STEmailAddresses).ChildText(wbxml.STSmtpAddress); smtp != testUser {
		t.Errorf("SmtpAddress = %q, want %q", smtp, testUser)
	}
}

// TestSettingsOofRoundTrip sets a global OOF reply, then reads it back.
func TestSettingsOofRoundTrip(t *testing.T) {
	ts, _ := seededServer(t)
	set := wbxml.Elem(wbxml.STOof, wbxml.Elem(wbxml.STSet,
		wbxml.Str(wbxml.STOofState, "1"),
		oofMessageNode(wbxml.STAppliesToInternal, "1", "I am away")))
	_, root := postCommand(t, ts, "Settings", settingsReq(set))
	if oof := root.Child(wbxml.STOof); oof == nil || oof.ChildText(wbxml.STStatus) != "1" {
		t.Fatalf("Oof Set status not 1")
	}

	_, root = postCommand(t, ts, "Settings", settingsReq(wbxml.Elem(wbxml.STOof, wbxml.Empty(wbxml.STGet))))
	get := root.Child(wbxml.STOof).Child(wbxml.STGet)
	if get.ChildText(wbxml.STOofState) != "1" {
		t.Errorf("OofState = %q, want 1", get.ChildText(wbxml.STOofState))
	}
	internal := oofMessageFor(get, wbxml.STAppliesToInternal)
	if internal == nil || internal.ChildText(wbxml.STReplyMessage) != "I am away" {
		t.Errorf("internal reply not round-tripped: %+v", internal)
	}
}

// TestSettingsOofTimeBased confirms a scheduled OOF round-trips its window.
func TestSettingsOofTimeBased(t *testing.T) {
	ts, dir := seededServer(t)
	set := wbxml.Elem(wbxml.STOof, wbxml.Elem(wbxml.STSet,
		wbxml.Str(wbxml.STOofState, "2"),
		wbxml.Str(wbxml.STStartTime, "2026-06-19T00:00:00.000Z"),
		wbxml.Str(wbxml.STEndTime, "2026-06-26T00:00:00.000Z"),
		oofMessageNode(wbxml.STAppliesToInternal, "1", "scheduled")))
	postCommand(t, ts, "Settings", settingsReq(set))

	cfg := storedOOF(t, dir)
	wantStart := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC).Unix()
	wantEnd := time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC).Unix()
	if cfg.Start != wantStart || cfg.End != wantEnd {
		t.Fatalf("stored window %d-%d, want %d-%d", cfg.Start, cfg.End, wantStart, wantEnd)
	}

	_, root := postCommand(t, ts, "Settings", settingsReq(wbxml.Elem(wbxml.STOof, wbxml.Empty(wbxml.STGet))))
	get := root.Child(wbxml.STOof).Child(wbxml.STGet)
	if get.ChildText(wbxml.STOofState) != "2" {
		t.Errorf("OofState = %q, want 2", get.ChildText(wbxml.STOofState))
	}
	if get.ChildText(wbxml.STStartTime) != "2026-06-19T00:00:00.000Z" {
		t.Errorf("StartTime = %q, want round-tripped", get.ChildText(wbxml.STStartTime))
	}
	if get.ChildText(wbxml.STEndTime) != "2026-06-26T00:00:00.000Z" {
		t.Errorf("EndTime = %q, want round-tripped", get.ChildText(wbxml.STEndTime))
	}
}

// TestSettingsOofRefusesABrokenWindow sends Oof Sets the [MS-ASCMD] rules refuse:
// one of StartTime/EndTime alone is Status 2 (2.2.3.176.2), an OofState outside 0-2
// (2.2.3.124), an unparseable time or a time-based state with no window is 5, and
// a window ending before it starts is 6. None may change the stored OOF, which is
// left off: a refused time-based Set must not leave a reply on with no end.
func TestSettingsOofRefusesABrokenWindow(t *testing.T) {
	ts, dir := seededServer(t)
	start, end := "2026-06-19T00:00:00.000Z", "2026-06-26T00:00:00.000Z"
	cases := []struct {
		name, status string
		fields       []*wbxml.Node
	}{
		{"start only", "2", []*wbxml.Node{wbxml.Str(wbxml.STOofState, "2"), wbxml.Str(wbxml.STStartTime, start)}},
		{"end only", "2", []*wbxml.Node{wbxml.Str(wbxml.STOofState, "2"), wbxml.Str(wbxml.STEndTime, end)}},
		{"unknown state", "5", []*wbxml.Node{wbxml.Str(wbxml.STOofState, "7")}},
		{"time-based without a window", "5", []*wbxml.Node{wbxml.Str(wbxml.STOofState, "2")}},
		{"unparseable time", "5", []*wbxml.Node{wbxml.Str(wbxml.STOofState, "2"),
			wbxml.Str(wbxml.STStartTime, "tomorrow"), wbxml.Str(wbxml.STEndTime, end)}},
		{"ends before it starts", "6", []*wbxml.Node{wbxml.Str(wbxml.STOofState, "2"),
			wbxml.Str(wbxml.STStartTime, end), wbxml.Str(wbxml.STEndTime, start)}},
	}
	for _, c := range cases {
		fields := append(c.fields, oofMessageNode(wbxml.STAppliesToInternal, "1", "away"))
		_, root := postCommand(t, ts, "Settings", settingsReq(wbxml.Elem(wbxml.STOof, wbxml.Elem(wbxml.STSet, fields...))))
		if got := root.Child(wbxml.STOof).ChildText(wbxml.STStatus); got != c.status {
			t.Errorf("%s: Oof Status = %q, want %s", c.name, got, c.status)
		}
	}
	if cfg := storedOOF(t, dir); cfg.Enabled || cfg.InternalReply != "" {
		t.Errorf("a refused Set changed the stored OOF: %+v", cfg)
	}
}

// TestSettingsOofExternalAudience confirms the external audience round-trips
// through EAS's two external buckets: a Known-only Set (only ExternalKnown
// enabled) enables just the ExternalKnown bucket on Get, while an All Set
// (ExternalUnknown also enabled) enables both, the single reply text reaches both
// buckets either way.
func TestSettingsOofExternalAudience(t *testing.T) {
	ts, dir := seededServer(t)

	// Known-only: the client enables ExternalKnown but not ExternalUnknown.
	known := wbxml.Elem(wbxml.STOof, wbxml.Elem(wbxml.STSet,
		wbxml.Str(wbxml.STOofState, "1"),
		oofMessageNode(wbxml.STAppliesToInternal, "1", "internal"),
		oofMessageNode(wbxml.STAppliesToExternalKnown, "1", "external"),
		oofMessageNode(wbxml.STAppliesToExternalUnknown, "0", "external")))
	postCommand(t, ts, "Settings", settingsReq(known))

	cfg := storedOOF(t, dir)
	wantEq(t, "external replies after the known-only Set", cfg.ExternalEnabled, true)
	wantEq(t, "the audience after the known-only Set", cfg.ExternalAudience, objectstore.OOFExternalKnown)

	kn, un := oofBucketsOf(t, ts)
	wantEq(t, "the known bucket after the known-only Set", kn.ChildText(wbxml.STEnabled), "1")
	wantEq(t, "the unknown bucket after the known-only Set", un.ChildText(wbxml.STEnabled), "0")
	// The single stored reply text reaches both buckets.
	wantEq(t, "the known bucket's reply", kn.ChildText(wbxml.STReplyMessage), "external")
	wantEq(t, "the unknown bucket's reply", un.ChildText(wbxml.STReplyMessage), "external")

	// All: the client also enables ExternalUnknown.
	all := wbxml.Elem(wbxml.STOof, wbxml.Elem(wbxml.STSet,
		wbxml.Str(wbxml.STOofState, "1"),
		oofMessageNode(wbxml.STAppliesToExternalKnown, "1", "external"),
		oofMessageNode(wbxml.STAppliesToExternalUnknown, "1", "external")))
	postCommand(t, ts, "Settings", settingsReq(all))

	cfg = storedOOF(t, dir)
	wantEq(t, "external replies after the all Set", cfg.ExternalEnabled, true)
	wantEq(t, "the audience after the all Set", cfg.ExternalAudience, objectstore.OOFExternalAll)

	kn, un = oofBucketsOf(t, ts)
	wantEq(t, "the known bucket after the all Set", kn.ChildText(wbxml.STEnabled), "1")
	wantEq(t, "the unknown bucket after the all Set", un.ChildText(wbxml.STEnabled), "1")
}

// storedOOF reads the mailbox's stored OOF settings.
func storedOOF(t *testing.T, dir string) objectstore.OOFSettings {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer mustClose(t, st)
	cfg, err := st.GetOOFSettings()
	mustNoErr(t, "read the OOF settings", err)
	return cfg
}

// oofBucketsOf runs an Oof Get and returns its two external OofMessage blocks.
func oofBucketsOf(t *testing.T, ts *httptest.Server) (known, unknown *wbxml.Node) {
	t.Helper()
	_, root := postCommand(t, ts, "Settings", settingsReq(wbxml.Elem(wbxml.STOof, wbxml.Empty(wbxml.STGet))))
	get := root.Child(wbxml.STOof).Child(wbxml.STGet)
	return oofMessageFor(get, wbxml.STAppliesToExternalKnown), oofMessageFor(get, wbxml.STAppliesToExternalUnknown)
}

// TestSettingsOofPreservesSubject confirms an Oof Set (which carries no subject and
// here no external buckets) read-merges, leaving the per-audience subjects and the
// external audience set elsewhere intact.
func TestSettingsOofPreservesSubject(t *testing.T) {
	ts, dir := seededServer(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetOOFSettings(objectstore.OOFSettings{
		InternalSubject:  "On vacation",
		ExternalSubject:  "Out of office",
		ExternalAudience: objectstore.OOFExternalKnown,
	}); err != nil {
		t.Fatal(err)
	}
	mustNoErr(t, "close the store", st.Close())

	set :=wbxml.Elem(wbxml.STOof, wbxml.Elem(wbxml.STSet,
		wbxml.Str(wbxml.STOofState, "1"),
		oofMessageNode(wbxml.STAppliesToInternal, "1", "away")))
	postCommand(t, ts, "Settings", settingsReq(set))

	cfg := storedOOF(t, dir)
	if cfg.InternalSubject != "On vacation" || cfg.ExternalSubject != "Out of office" {
		t.Errorf("subjects not preserved: internal=%q external=%q", cfg.InternalSubject, cfg.ExternalSubject)
	}
	if cfg.ExternalAudience != objectstore.OOFExternalKnown {
		t.Errorf("ExternalAudience = %d, want preserved Known", cfg.ExternalAudience)
	}
	if !cfg.Enabled || cfg.InternalReply != "away" {
		t.Errorf("OOF Set did not apply: %+v", cfg)
	}
}

// TestSettingsDeviceInformation confirms a DeviceInformation Set is acknowledged.
func TestSettingsDeviceInformation(t *testing.T) {
	ts, _ := seededServer(t)
	set := wbxml.Elem(wbxml.STDeviceInformation, wbxml.Elem(wbxml.STSet,
		wbxml.Str(wbxml.STModel, "iPhone15")))
	_, root := postCommand(t, ts, "Settings", settingsReq(set))
	di := root.Child(wbxml.STDeviceInformation)
	if di == nil || di.ChildText(wbxml.STStatus) != "1" {
		t.Errorf("DeviceInformation Set not acked with Status 1: %+v", di)
	}
}
