package sendas

import "testing"

// TestHeaderFromSeparatesWhatAMessageClaims proves the three answers stay apart:
// a gate that reads "no claim" where the message in fact names someone lets that
// someone through. RFC 5322 3.6 allows one From field and 3.6.2 an address list in
// it; every form that can carry an address beside the checked one is invalid, in
// either field order, and so is a message or field that does not parse.
func TestHeaderFromSeparatesWhatAMessageClaims(t *testing.T) {
	const body = "To: c@external.test\r\nSubject: s\r\n\r\nhi\r\n"
	for _, tc := range []struct {
		name, raw string
		addr      string
		claim     FromClaim
	}{
		{"one address", "From: Alice <alice@hermex.test>\r\n" + body, "alice@hermex.test", FromOne},
		{"no From field", body, "", FromAbsent},
		{"empty From field", "From:\r\n" + body, "", FromInvalid},
		{"a list", "From: alice@hermex.test, bob@hermex.test\r\n" + body, "", FromInvalid},
		{"a folded list", "From: alice@hermex.test,\r\n bob@hermex.test\r\n" + body, "", FromInvalid},
		{"two fields, own first", "From: alice@hermex.test\r\nFrom: bob@hermex.test\r\n" + body, "", FromInvalid},
		{"two fields, other first", "From: bob@hermex.test\r\nFrom: alice@hermex.test\r\n" + body, "", FromInvalid},
		{"an unparseable field", "From: bob@hermex.test <\r\n" + body, "", FromInvalid},
		{"a header block that does not parse", "no colon here\r\nFrom: bob@hermex.test\r\n" + body, "", FromInvalid},
	} {
		addr, claim := HeaderFrom([]byte(tc.raw))
		if addr != tc.addr || claim != tc.claim {
			t.Errorf("%s: HeaderFrom = (%q, %d), want (%q, %d)", tc.name, addr, claim, tc.addr, tc.claim)
		}
	}
}
