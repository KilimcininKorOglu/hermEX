package oxcical

import (
	"regexp"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

var uidLine = regexp.MustCompile(`(?m)^UID:(.*)\r$`)

// exportedUID exports an appointment with no identity and returns its UID.
func exportedUID(t *testing.T) string {
	t.Helper()
	msg := &oxcmail.Message{}
	msg.Props.Set(mapi.PrMessageClass, "IPM.Appointment")
	msg.Props.Set(mapi.PrSubject, "no identity")
	raw, err := Export(msg, newResolver().opt())
	if err != nil {
		t.Fatal(err)
	}
	m := uidLine.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no UID line:\n%s", raw)
	}
	return string(m[1])
}

// TestAnEventWithoutIdentityGetsAUniqueUID proves two events that carry neither a
// UID nor a global object id do not share one: a shared UID makes a CalDAV client
// merge them into a single event (RFC 5545 section 3.8.4.7).
func TestAnEventWithoutIdentityGetsAUniqueUID(t *testing.T) {
	a, b := exportedUID(t), exportedUID(t)
	if a == "" || a == b {
		t.Errorf("two events without an identity exported UIDs %q and %q", a, b)
	}
}
