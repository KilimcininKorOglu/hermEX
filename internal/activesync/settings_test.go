package activesync

import (
	"testing"

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

	st, _ := objectstore.Open(dir)
	cfg, _ := st.GetOOFSettings()
	st.Close()
	if cfg.Start == 0 || cfg.End == 0 || cfg.End <= cfg.Start {
		t.Fatalf("schedule not stored: start=%d end=%d", cfg.Start, cfg.End)
	}

	_, root := postCommand(t, ts, "Settings", settingsReq(wbxml.Elem(wbxml.STOof, wbxml.Empty(wbxml.STGet))))
	get := root.Child(wbxml.STOof).Child(wbxml.STGet)
	if get.ChildText(wbxml.STOofState) != "2" {
		t.Errorf("OofState = %q, want 2", get.ChildText(wbxml.STOofState))
	}
	if get.ChildText(wbxml.STStartTime) != "2026-06-19T00:00:00.000Z" {
		t.Errorf("StartTime = %q, want round-tripped", get.ChildText(wbxml.STStartTime))
	}
}

// TestSettingsOofExternalAudience confirms the external audience round-trips
// through EAS's two external buckets: a Known-only Set (only ExternalKnown
// enabled) enables just the ExternalKnown bucket on Get, while an All Set
// (ExternalUnknown also enabled) enables both — the single reply text reaches both
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

	st, _ := objectstore.Open(dir)
	cfg, _ := st.GetOOFSettings()
	st.Close()
	if !cfg.ExternalEnabled || cfg.ExternalAudience != objectstore.OOFExternalKnown {
		t.Fatalf("known-only Set: ExternalEnabled=%v audience=%d, want enabled Known", cfg.ExternalEnabled, cfg.ExternalAudience)
	}

	_, root := postCommand(t, ts, "Settings", settingsReq(wbxml.Elem(wbxml.STOof, wbxml.Empty(wbxml.STGet))))
	get := root.Child(wbxml.STOof).Child(wbxml.STGet)
	kn := oofMessageFor(get, wbxml.STAppliesToExternalKnown)
	un := oofMessageFor(get, wbxml.STAppliesToExternalUnknown)
	if kn.ChildText(wbxml.STEnabled) != "1" || un.ChildText(wbxml.STEnabled) != "0" {
		t.Errorf("known-only Get: known enabled=%q unknown enabled=%q, want 1/0", kn.ChildText(wbxml.STEnabled), un.ChildText(wbxml.STEnabled))
	}
	if kn.ChildText(wbxml.STReplyMessage) != "external" || un.ChildText(wbxml.STReplyMessage) != "external" {
		t.Errorf("reply text must reach both buckets: known=%q unknown=%q", kn.ChildText(wbxml.STReplyMessage), un.ChildText(wbxml.STReplyMessage))
	}

	// All: the client also enables ExternalUnknown.
	all := wbxml.Elem(wbxml.STOof, wbxml.Elem(wbxml.STSet,
		wbxml.Str(wbxml.STOofState, "1"),
		oofMessageNode(wbxml.STAppliesToExternalKnown, "1", "external"),
		oofMessageNode(wbxml.STAppliesToExternalUnknown, "1", "external")))
	postCommand(t, ts, "Settings", settingsReq(all))

	st, _ = objectstore.Open(dir)
	cfg, _ = st.GetOOFSettings()
	st.Close()
	if !cfg.ExternalEnabled || cfg.ExternalAudience != objectstore.OOFExternalAll {
		t.Fatalf("all Set: ExternalEnabled=%v audience=%d, want enabled All", cfg.ExternalEnabled, cfg.ExternalAudience)
	}

	_, root = postCommand(t, ts, "Settings", settingsReq(wbxml.Elem(wbxml.STOof, wbxml.Empty(wbxml.STGet))))
	get = root.Child(wbxml.STOof).Child(wbxml.STGet)
	kn = oofMessageFor(get, wbxml.STAppliesToExternalKnown)
	un = oofMessageFor(get, wbxml.STAppliesToExternalUnknown)
	if kn.ChildText(wbxml.STEnabled) != "1" || un.ChildText(wbxml.STEnabled) != "1" {
		t.Errorf("all Get: known enabled=%q unknown enabled=%q, want 1/1", kn.ChildText(wbxml.STEnabled), un.ChildText(wbxml.STEnabled))
	}
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
	st.Close()

	set := wbxml.Elem(wbxml.STOof, wbxml.Elem(wbxml.STSet,
		wbxml.Str(wbxml.STOofState, "1"),
		oofMessageNode(wbxml.STAppliesToInternal, "1", "away")))
	postCommand(t, ts, "Settings", settingsReq(set))

	st, _ = objectstore.Open(dir)
	cfg, _ := st.GetOOFSettings()
	st.Close()
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
