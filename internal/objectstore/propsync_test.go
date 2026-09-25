package objectstore

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestSetMessagePropertiesReachesASyncedClient is the contract every protocol path
// depends on. SetMessageProperties is what the ROP message-status write, the
// ActiveSync Change command, the webmail category/follow-up/contact-photo writes and
// the rules engine all end up calling; ICS content sync reports a message as updated
// only when its change_number advances, so a property write that left the column
// alone never reached a client that had already synced the message.
func TestSetMessagePropertiesReachesASyncedClient(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	id, err := s.CreateMessage(fld, contactMsg("synced"))
	mustNoErr(t, "create the message", err)

	// The client has downloaded the message at its current change number.
	before := msgCN(t, s, id)
	seen := looseSet(before)

	var props mapi.PropertyValues
	props.Set(mapi.PrSubject, "edited after the client synced")
	mustNoErr(t, "set the property", s.SetMessageProperties(id, props))

	if after := msgCN(t, s, id); after <= before {
		t.Fatalf("change number stayed at %d after the property write, so no client sees it", after)
	}
	res, err := s.GetContentSync(ContentSyncRequest{
		FolderID: fld, Given: looseSet(uint64(id)), Seen: seen, SeenFAI: nil, Read: nil,
	})
	mustNoErr(t, "content sync", err)
	eqSet(t, "UpdatedMIDs", res.UpdatedMIDs, uint64(id))
}

// TestRecipientWriteReachesASyncedClient covers a meeting organizer's attendee
// tracking: the response status and proposed times live on the recipient row, and a
// write there must advance the message's change number like any property write.
func TestRecipientWriteReachesASyncedClient(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDCalendar)
	id, err := s.CreateMessage(fld, &oxcmail.Message{
		Props:      mapi.PropertyValues{{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"}},
		Recipients: []mapi.PropertyValues{{{Tag: mapi.PrSmtpAddress, Value: "bob@hermex.test"}}},
	})
	mustNoErr(t, "create the meeting", err)
	recips, err := s.ListRecipients(id)
	mustNoErr(t, "list recipients", err)

	before := msgCN(t, s, id)
	seen := looseSet(before)
	mustNoErr(t, "set the recipient property",
		s.SetRecipientProperties(recips[0].ID, mapi.PropertyValues{{Tag: mapi.PrRecipientProposed, Value: true}}))

	if after := msgCN(t, s, id); after <= before {
		t.Fatalf("change number stayed at %d after the recipient write, so no client sees it", after)
	}
	res, err := s.GetContentSync(ContentSyncRequest{
		FolderID: fld, Given: looseSet(uint64(id)), Seen: seen, SeenFAI: nil, Read: nil,
	})
	mustNoErr(t, "content sync", err)
	eqSet(t, "UpdatedMIDs", res.UpdatedMIDs, uint64(id))
}

// TestRuleTagReachesASyncedClient is the same claim through the rules engine, which
// is where it matters most: running rules over a folder after the fact touches mail
// every client synced long ago. A tag action that does not advance the change number
// writes the property into the database and shows it to nobody.
func TestRuleTagReachesASyncedClient(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	m := deliverTo(t, s, inbox, ruleMsg("Project update", "lead@acme.com", ""))
	before := msgCN(t, s, m.ID)

	mustAddRule(t, s, Rule{
		FolderID: inbox, Name: "flag projects", State: mapi.RuleStateEnabled,
		Condition: RuleSubjectContains("project"),
		Actions: mapi.RuleActions{Blocks: []mapi.ActionBlock{{
			Type: mapi.OpTag,
			Data: mapi.TaggedPropVal{Tag: mapi.PrImportance, Value: int32(2)},
		}}},
	})

	res, err := s.RunRulesIn(RunRulesOptions{RuleFolderID: inbox, TargetFolderID: inbox}, 0)
	mustNoErr(t, "run rules in", err)
	wantEq(t, "messages the run affected", res.Affected, 1)

	props, err := s.GetMessageProperties(m.ID, mapi.PrImportance)
	mustNoErr(t, "read the tagged property", err)
	if v, ok := props.Get(mapi.PrImportance); !ok || v != int32(2) {
		t.Fatalf("the tag action wrote %v (present=%t), want 2", v, ok)
	}
	if after := msgCN(t, s, m.ID); after <= before {
		t.Errorf("change number stayed at %d after the tag, so no synced client sees it", after)
	}
}
