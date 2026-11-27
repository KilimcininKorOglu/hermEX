package directory

import (
	"strings"
	"testing"
)

// TestRoutableAddressesRespectTheSMTPLimits: RFC 5321 4.5.3.1.1 caps a local
// part at 64 octets and 4.5.3.1.3 caps a path at 256, so an address over 254
// octets can never be sent to or received from. Accepting one stores a
// forward, contact or access rule that can never apply.
func TestRoutableAddressesRespectTheSMTPLimits(t *testing.T) {
	label := strings.Repeat("d", 60)
	// longDomain is 248 octets; fits is 250, the over-long address 259, and the
	// first refused address has a 65-octet local part.
	longDomain := strings.Join([]string{label, label, label, label}, ".") + ".test"
	fits := "a@" + longDomain
	for _, addr := range []string{
		strings.Repeat("l", 65) + "@x.test",
		strings.Repeat("l", 10) + "@" + longDomain,
	} {
		if ValidateForwardDestination(addr) == nil {
			t.Errorf("forward destination of %d octets accepted", len(addr))
		}
		if ValidateContactAddress(addr) == nil {
			t.Errorf("contact address of %d octets accepted", len(addr))
		}
		if ValidateAccessPattern(addr) == nil {
			t.Errorf("access pattern of %d octets accepted", len(addr))
		}
	}
	for _, addr := range []string{strings.Repeat("l", 64) + "@x.test", fits} {
		if err := ValidateForwardDestination(addr); err != nil {
			t.Errorf("an address of %d octets within the limits was refused: %v", len(addr), err)
		}
	}
}
