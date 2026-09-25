package objectstore

import (
	"errors"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// testResponseTag stands in for the named response status an iTIP reply writes on
// an attendee.
const testResponseTag = mapi.PropTag(0x80010003)

func attendee(addr, name string) mapi.PropertyValues {
	return mapi.PropertyValues{
		{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
		{Tag: mapi.PrAddrType, Value: "SMTP"},
		{Tag: mapi.PrEmailAddress, Value: addr},
		{Tag: mapi.PrSmtpAddress, Value: addr},
		{Tag: mapi.PrDisplayName, Value: name},
	}
}

// meetingWithBobAndCarol stores an appointment with two attendees, Bob carrying
// the response status a reply stored.
func meetingWithBobAndCarol(t *testing.T, s *Store) int64 {
	t.Helper()
	bob := append(attendee("bob@example.test", "Bob"), mapi.TaggedPropVal{Tag: testResponseTag, Value: int32(3)})
	msg := &oxcmail.Message{
		Props:      mapi.PropertyValues{{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"}},
		Recipients: []mapi.PropertyValues{bob, attendee("carol@example.test", "Carol")},
	}
	id, err := s.CreateMessage(int64(mapi.PrivateFIDCalendar), msg)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestReplaceRecipientsKeepsWhatAnAttendeeLearned replaces Bob and Carol with Bob
// (under a new display name, in upper case) and Dave: the message keeps its id and
// gains a change number, Carol goes, Dave arrives, and Bob keeps the response
// status the new bag knows nothing about while taking the new display name.
func TestReplaceRecipientsKeepsWhatAnAttendeeLearned(t *testing.T) {
	s := openSeededStore(t)
	id := meetingWithBobAndCarol(t, s)
	before := msgCN(t, s, id)

	if err := s.ReplaceRecipients(id, []mapi.PropertyValues{
		attendee("BOB@example.test", "Robert"),
		attendee("dave@example.test", "Dave"),
	}); err != nil {
		t.Fatal(err)
	}

	msg, err := s.OpenMessage(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Recipients) != 2 {
		t.Fatalf("message has %d recipients, want 2", len(msg.Recipients))
	}
	bob, dave := msg.Recipients[0], msg.Recipients[1]
	if got, _ := stringProp(bob, mapi.PrDisplayName); got != "Robert" {
		t.Errorf("Bob's display name = %q, want the new Robert", got)
	}
	if v, _ := bob.Get(testResponseTag); v != int32(3) {
		t.Errorf("Bob's response status = %v, want the stored 3", v)
	}
	if got := recipientAddress(dave); got != "dave@example.test" {
		t.Errorf("second recipient = %q, want dave@example.test", got)
	}
	if _, ok := dave.Get(testResponseTag); ok {
		t.Error("Dave took a response status he never sent")
	}
	if after := msgCN(t, s, id); after <= before {
		t.Errorf("change number %d did not advance past %d", after, before)
	}
}

// TestReplaceRecipientsCarriesOneRowOnce gives two new rows the address of one old
// row: only the first inherits its properties.
func TestReplaceRecipientsCarriesOneRowOnce(t *testing.T) {
	s := openSeededStore(t)
	id := meetingWithBobAndCarol(t, s)
	if err := s.ReplaceRecipients(id, []mapi.PropertyValues{
		attendee("bob@example.test", "Bob"),
		attendee("bob@example.test", "Bob again"),
	}); err != nil {
		t.Fatal(err)
	}
	msg, err := s.OpenMessage(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := msg.Recipients[1].Get(testResponseTag); ok {
		t.Error("the second row with Bob's address also inherited his response status")
	}
}

// TestReplaceRecipientsRefusesAMissingMessage reports ErrNotFound and writes
// nothing for an id no message has.
func TestReplaceRecipientsRefusesAMissingMessage(t *testing.T) {
	s := openSeededStore(t)
	err := s.ReplaceRecipients(1<<40, []mapi.PropertyValues{attendee("bob@example.test", "Bob")})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
