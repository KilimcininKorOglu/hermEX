package nspi

import (
	"slices"
	"testing"

	"hermex/internal/directory"
)

// tenantGAL is a directory.GAL that answers with only the entries in the
// caller's own domain, the narrowest shape of the scoping a real directory
// applies. It stands in for SQLDirectory here so this package can prove it
// carries the caller through rather than reproving the SQL.
type tenantGAL []string

func (g tenantGAL) SearchGAL(caller, _ string, _ int) ([]directory.GALEntry, error) {
	dom := domainOf(caller)
	if dom == "" {
		return nil, nil
	}
	var out []directory.GALEntry
	for _, addr := range g {
		if domainOf(addr) == dom {
			out = append(out, directory.GALEntry{DisplayName: addr, Address: addr, StorePath: "/mb/" + addr})
		}
	}
	return out, nil
}

// domainOf returns the part of an address after the '@', or "" when there is none.
func domainOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == '@' {
			return addr[i+1:]
		}
	}
	return ""
}

// snapshotAddresses returns the addresses in the address book caller sees.
func snapshotAddresses(s *Server, caller string) []string {
	var out []string
	for _, u := range s.snapshot(caller).users {
		out = append(out, u.smtp)
	}
	return out
}

// TestSnapshotIsPerCaller proves the NSPI address book is built for whoever is
// asking. The server bound one GAL at construction and every op read the same
// snapshot, so an Outlook client in one tenant enumerated every mailbox the
// server hosted, in every other tenant.
func TestSnapshotIsPerCaller(t *testing.T) {
	s := NewServer(tenantGAL{
		"alice@acme.test", "bob@acme.test", "eve@other.test",
	}, testGUID)

	acme := snapshotAddresses(s, "alice@acme.test")
	if !slices.Contains(acme, "bob@acme.test") {
		t.Errorf("a colleague is missing from the address book: %v", acme)
	}
	if slices.Contains(acme, "eve@other.test") {
		t.Errorf("the address book carries another tenant's mailbox: %v", acme)
	}

	other := snapshotAddresses(s, "eve@other.test")
	if !slices.Contains(other, "eve@other.test") || len(other) != 1 {
		t.Errorf("the other tenant's address book = %v, want only its own mailbox", other)
	}
}

// TestSnapshotWithoutACallerIsEmpty proves the transport-side omission fails
// closed: an op reached without an authenticated identity gets no address book
// rather than the whole deployment.
func TestSnapshotWithoutACallerIsEmpty(t *testing.T) {
	s := NewServer(tenantGAL{"alice@acme.test", "eve@other.test"}, testGUID)
	if got := snapshotAddresses(s, ""); len(got) != 0 {
		t.Errorf("an unauthenticated snapshot returned %v, want nothing", got)
	}
}

// TestScopedMIDsAddressTheCallersOwnEntries proves the per-caller snapshot stays
// internally consistent: MIds are assigned by position, so a caller's MId must
// resolve to that caller's own entry and never index into a neighbour's book.
func TestScopedMIDsAddressTheCallersOwnEntries(t *testing.T) {
	s := NewServer(tenantGAL{
		"alice@acme.test", "bob@acme.test", "eve@other.test",
	}, testGUID)

	g := s.snapshot("eve@other.test")
	if len(g.users) != 1 {
		t.Fatalf("the other tenant sees %d entries, want 1", len(g.users))
	}
	u, ok := g.byMID(midBase)
	if !ok || u.smtp != "eve@other.test" {
		t.Errorf("the first MId resolved to %+v, want the caller's own entry", u)
	}
	// One past the caller's own book must not resolve into anything.
	if _, ok := g.byMID(midBase + 1); ok {
		t.Error("an MId past the caller's address book resolved to an entry")
	}
}
