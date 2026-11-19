package mta

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/dkimsign"
	"hermex/internal/relay"
	"hermex/internal/smtp"
)

// fakeExpander is a configurable MListExpander: each entry maps a list address to
// its direct members and the posting result. An unknown address is "not a list".
type fakeExpander map[string]struct {
	members []string
	res     directory.MListResult
}

func (f fakeExpander) ExpandMList(listAddr, _ string) ([]string, directory.MListResult, error) {
	l, ok := f[strings.ToLower(strings.TrimSpace(listAddr))]
	if !ok {
		return nil, directory.MListNone, nil
	}
	return l.members, l.res, nil
}

func ok(members ...string) struct {
	members []string
	res     directory.MListResult
} {
	return struct {
		members []string
		res     directory.MListResult
	}{members, directory.MListOK}
}

// TestExpandMailingListRecursion proves the MTA-side expansion: nested lists
// flatten, the posting privilege gates the top level, membership cycles
// terminate, and a recipient is delivered at most once however the lists overlap.
func TestExpandMailingListRecursion(t *testing.T) {
	const from = "sender@out.test"

	t.Run("not a list passes through", func(t *testing.T) {
		_, isList, _, _ := expandMailingList(fakeExpander{}, from, "alice@local")
		if isList {
			t.Error("an ordinary address was treated as a list")
		}
	})

	t.Run("flat list returns members", func(t *testing.T) {
		f := fakeExpander{"team@local": ok("alice@local", "bob@local")}
		leaves, isList, res, _ := expandMailingList(f, from, "team@local")
		wantEq(t, "the address is a list", isList, true)
		wantEq(t, "the expansion result", res, directory.MListOK)
		wantMembers(t, "the flat expansion", leaves, "alice@local", "bob@local")
	})

	t.Run("nested list flattens", func(t *testing.T) {
		f := fakeExpander{
			"all@local":  ok("alice@local", "team@local"),
			"team@local": ok("bob@local", "carol@local"),
		}
		leaves, _, _, _ := expandMailingList(f, from, "all@local")
		wantMembers(t, "the nested expansion", leaves, "alice@local", "bob@local", "carol@local")
	})

	t.Run("membership cycle terminates and de-duplicates", func(t *testing.T) {
		f := fakeExpander{
			"a@local": ok("alice@local", "b@local"),
			"b@local": ok("bob@local", "a@local", "alice@local"), // loops back + repeats alice
		}
		leaves, _, _, _ := expandMailingList(f, from, "a@local")
		wantMembers(t, "the cyclic expansion (no loop, deduped)", leaves, "alice@local", "bob@local")
	})

	t.Run("top-level posting refusal is reported", func(t *testing.T) {
		f := fakeExpander{"closed@local": {res: directory.MListPrivilInternal}}
		leaves, isList, res, _ := expandMailingList(f, from, "closed@local")
		if !isList || res != directory.MListPrivilInternal || leaves != nil {
			t.Errorf("got (%v, list=%v, res=%d), want (nil, list, PRIVIL_INTERNAL)", leaves, isList, res)
		}
	})

	t.Run("nested refusal drops the sub-list's members", func(t *testing.T) {
		f := fakeExpander{
			"a@local":      ok("alice@local", "closed@local"),
			"closed@local": {members: []string{"secret@local"}, res: directory.MListPrivilDomain},
		}
		leaves, _, _, _ := expandMailingList(f, from, "a@local")
		wantMembers(t, "the expansion (a refused sub-list contributes nothing)", leaves, "alice@local")
	})
}

// wantMembers checks a list expansion holds exactly the members wanted, in any
// order.
func wantMembers(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// mlistDir is a directory that both resolves mailboxes (via the embedded static
// accounts) and expands lists, for exercising Rcpt's list routing.
type mlistDir struct {
	directory.StaticAccounts
	lists fakeExpander
}

func (d mlistDir) ExpandMList(listAddr, from string) ([]string, directory.MListResult, error) {
	return d.lists.ExpandMList(listAddr, from)
}

// TestRcptRoutesListMembers proves Rcpt expands a list recipient into a delivery
// target per resolvable member, refuses a posting-denied list with an error, and
// silently drops a member that no longer resolves (a stale member must not fail
// the whole list).
func TestRcptRoutesListMembers(t *testing.T) {
	accounts := mlistDir{
		StaticAccounts: directory.StaticAccounts{
			"alice@local": {MailboxPath: "/mb/alice"},
			"bob@local":   {MailboxPath: "/mb/bob"},
		},
		lists: fakeExpander{
			"team@local":   ok("alice@local", "bob@local"),
			"stale@local":  ok("alice@local", "ghost@local"), // ghost has no mailbox
			"closed@local": {res: directory.MListPrivilInternal},
		},
	}

	t.Run("members become targets", func(t *testing.T) {
		s := &session{accounts: accounts, from: "x@out.test"}
		if err := s.Rcpt("team@local", smtp.RcptParams{}); err != nil {
			t.Fatalf("Rcpt(list) = %v, want nil", err)
		}
		if len(s.targets) != 2 {
			t.Errorf("targets = %d, want 2 (alice, bob)", len(s.targets))
		}
	})

	t.Run("posting refusal is a 550", func(t *testing.T) {
		s := &session{accounts: accounts, from: "x@out.test"}
		if err := s.Rcpt("closed@local", smtp.RcptParams{}); err == nil {
			t.Error("Rcpt to a posting-denied list should be refused")
		}
		if len(s.targets) != 0 {
			t.Errorf("a refused list still produced %d targets", len(s.targets))
		}
	})

	t.Run("a stale member is dropped, not fatal", func(t *testing.T) {
		s := &session{accounts: accounts, from: "x@out.test"}
		if err := s.Rcpt("stale@local", smtp.RcptParams{}); err != nil {
			t.Fatalf("Rcpt with a stale member = %v, want nil", err)
		}
		if len(s.targets) != 1 {
			t.Errorf("targets = %d, want 1 (alice only; ghost dropped)", len(s.targets))
		}
	})
}

// countingSigner stands in for the DKIM signer and counts its calls.
type countingSigner struct{ calls int }

func (c *countingSigner) Sign(b []byte) []byte { c.calls++; return b }

// staticKeys is a KeyProvider holding one domain's signing key.
type staticKeys struct {
	domain string
	pem    []byte
}

func (k staticKeys) DKIMKey(domain string) ([]byte, string, bool, error) {
	if domain != k.domain {
		return nil, "", false, nil
	}
	return k.pem, "s1", true, nil
}

// testForwarding is a Forwarding for the domain local with an RSA key and an
// SRS secret, resolving no DNS.
func testForwarding(t *testing.T) *Forwarding {
	t.Helper()
	pem, _, err := dkimsign.GenerateKey(dkimsign.KeyRSA)
	if err != nil {
		t.Fatal(err)
	}
	return &Forwarding{Keys: staticKeys{domain: "local", pem: pem}, SRSSecret: []byte("srs secret"),
		AuthServID: "mx.local", Lookup: func(string) ([]string, error) { return nil, errors.New("no DNS in tests") }}
}

// listRelay posts to a list with an external member through a session with the
// given authenticated user. It returns how often the author signer ran and the
// one queued delivery.
func listRelay(t *testing.T, authUser string) (int, relay.Item) {
	t.Helper()
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sp.Close(); err != nil {
			t.Error(err)
		}
	})
	cs := &countingSigner{}
	sp.Signer = cs
	accounts := mlistDir{
		StaticAccounts: directory.StaticAccounts{"alice@local": {MailboxPath: "/mb/alice"}},
		lists:          fakeExpander{"team@local": ok("alice@local", "ext@remote.test")},
	}
	s := &session{accounts: accounts, from: "x@out.test", authUser: authUser, spool: sp, forwarding: testForwarding(t)}
	if err := s.Rcpt("team@local", smtp.RcptParams{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw := []byte("From: ceo@local\r\nSubject: wire money\r\n\r\nbody\r\n")
	if err := s.queueRelay(raw, now); err != nil {
		t.Fatal(err)
	}
	if err := s.queueForwards(raw, now); err != nil {
		t.Fatal(err)
	}
	items, err := sp.Claim(now, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("queued %d deliveries (%v), want the external member", len(items), err)
	}
	return cs.calls, items[0]
}

// TestUnauthenticatedListRelayIsNotAuthorSigned posts, without authentication,
// a message claiming From: ceo@local to a list with an external member. The
// signer keys on the From domain, so signing it would hand a forgery the hosted
// domain's valid DKIM signature (RFC 6376 section 1.1: the signer takes
// responsibility for the message). The list's domain vouches for the relay with
// an ARC set (RFC 8617) and an SRS envelope sender at its own domain instead. An
// authenticated sender's From passed sendas, so that relay is author-signed and
// keeps its envelope sender.
func TestUnauthenticatedListRelayIsNotAuthorSigned(t *testing.T) {
	n, item := listRelay(t, "")
	if n != 0 {
		t.Errorf("an unauthenticated list relay was author-signed %d times", n)
	}
	if !strings.HasPrefix(item.From, "SRS0=") || !strings.HasSuffix(item.From, "=out.test=x@local") {
		t.Errorf("envelope sender = %q, want an SRS0 address at the list domain", item.From)
	}
	if !strings.HasPrefix(string(item.Body), "ARC-Seal: i=1; a=rsa-sha256; d=local; s=s1;") {
		t.Errorf("the relayed copy carries no ARC set by the list domain:\n%s", item.Body)
	}
	n, item = listRelay(t, "alice@local")
	if n != 1 || item.From != "x@out.test" || strings.Contains(string(item.Body), "ARC-Seal") {
		t.Errorf("authenticated list relay: signed %d times, envelope %q, sealed %v; want 1, the sender, unsealed",
			n, item.From, strings.Contains(string(item.Body), "ARC-Seal"))
	}
}

// TestExpandRecipientListCollectsRefused proves the batch expander (used by
// DeliverAndRelay) flattens list members and separates posting-refused lists.
func TestExpandRecipientListCollectsRefused(t *testing.T) {
	accounts := mlistDir{
		StaticAccounts: directory.StaticAccounts{"alice@local": {MailboxPath: "/mb/alice"}},
		lists: fakeExpander{
			"team@local":   ok("alice@local", "bob@local"),
			"closed@local": {res: directory.MListPrivilDomain},
		},
	}
	leaves, refused := expandRecipientList(accounts, "x@out.test",
		[]string{"team@local", "closed@local", "dave@elsewhere.test"})
	slices.Sort(leaves)
	if want := []string{"alice@local", "bob@local", "dave@elsewhere.test"}; !slices.Equal(leaves, want) {
		t.Errorf("leaves = %v, want %v", leaves, want)
	}
	if !slices.Equal(refused, []string{"closed@local"}) {
		t.Errorf("refused = %v, want [closed@local]", refused)
	}
}
