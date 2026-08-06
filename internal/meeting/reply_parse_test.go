package meeting

import "testing"

// TestParseAttendeeReadsPartStat proves the responder's participation status is
// actually read off the ATTENDEE line. The parameter list was previously sliced
// from the property name up to the line's FIRST semicolon, which is where the
// parameters begin rather than end, so it was always empty and the organizer's
// Tracking tab silently never learned who accepted or declined.
func TestParseAttendeeReadsPartStat(t *testing.T) {
	cases := []struct {
		name, line, wantAddr, wantStat string
	}{
		{
			name:     "partstat first",
			line:     "ATTENDEE;PARTSTAT=ACCEPTED;CN=Bob:mailto:bob@hermex.test",
			wantAddr: "bob@hermex.test", wantStat: "ACCEPTED",
		},
		{
			name:     "partstat after another parameter",
			line:     "ATTENDEE;CN=Bob;PARTSTAT=DECLINED:mailto:bob@hermex.test",
			wantAddr: "bob@hermex.test", wantStat: "DECLINED",
		},
		{
			name:     "tentative with a role parameter",
			line:     "ATTENDEE;ROLE=REQ-PARTICIPANT;PARTSTAT=TENTATIVE:mailto:b@hermex.test",
			wantAddr: "b@hermex.test", wantStat: "TENTATIVE",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, stat := parseAttendee([]byte("BEGIN:VEVENT\r\n" + c.line + "\r\nEND:VEVENT\r\n"))
			if addr != c.wantAddr {
				t.Errorf("address = %q, want %q", addr, c.wantAddr)
			}
			if stat != c.wantStat {
				t.Errorf("partstat = %q, want %q", stat, c.wantStat)
			}
		})
	}
}

// TestParseAttendeeHandlesNoParameters proves a parameterless ATTENDEE line is
// parsed rather than panicking. The old offset was computed from a semicolon
// index of -1, so a perfectly valid reply crashed the delivery pass; the recover
// upstream kept mail flowing but abandoned the rest of that pass.
func TestParseAttendeeHandlesNoParameters(t *testing.T) {
	addr, stat := parseAttendee([]byte("BEGIN:VEVENT\r\nATTENDEE:mailto:bob@hermex.test\r\nEND:VEVENT\r\n"))
	if addr != "bob@hermex.test" {
		t.Errorf("address = %q, want bob@hermex.test", addr)
	}
	if stat != "" {
		t.Errorf("partstat = %q, want empty (the line carries none)", stat)
	}
}

// TestParseAttendeePartStatDrivesTheResponse ties the parsed value to the effect
// it is read for: an accepted reply must map to a response status the organizer's
// tracking can store, and an empty one must map to no update.
func TestParseAttendeePartStatDrivesTheResponse(t *testing.T) {
	_, stat := parseAttendee([]byte("ATTENDEE;PARTSTAT=ACCEPTED:mailto:b@hermex.test\r\n"))
	if got := partstatResponse(stat); got == 0 {
		t.Error("an accepted reply mapped to no response status; tracking would never update")
	}
	if got := partstatResponse(""); got != 0 {
		t.Errorf("an absent partstat mapped to %d, want 0 (no update)", got)
	}
}
