package mapihttp

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
)

// delegatingAccounts is StaticAccounts that also keeps per-mailbox delegate lists
// in memory, so the MAPI/HTTP ModLinkAtt write path can be exercised end to end
// (StaticAccounts alone is not a delegate writer, so ModLinkAtt would be
// unsupported). The map is keyed by the delegator's lowercased SMTP.
type delegatingAccounts struct {
	directory.StaticAccounts
	delegates map[string][]string
}

func (d delegatingAccounts) Delegates(userAddr string) ([]string, error) {
	return d.delegates[strings.ToLower(userAddr)], nil
}

func (d delegatingAccounts) SetDelegates(userAddr string, list []string) error {
	d.delegates[strings.ToLower(userAddr)] = list
	return nil
}

// ephemeralEID builds an EphemeralEntryID carrying a MId at offset 28.
func ephemeralEID(mid uint32) []byte {
	b := make([]byte, 32)
	b[0] = 0x87 // ENTRYID_TYPE_EPHEMERAL
	binary.LittleEndian.PutUint32(b[28:], mid)
	return b
}

// modLinkAttBody frames a ModLinkAtt request: flags + proptag + mid + an entry-id
// binary array (present byte, count, each {cb, bytes}) + empty auxin.
func modLinkAttBody(flags, proptag, mid uint32, entryIDs [][]byte) []byte {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, flags)
	b = binary.LittleEndian.AppendUint32(b, proptag)
	b = binary.LittleEndian.AppendUint32(b, mid)
	if len(entryIDs) == 0 {
		b = append(b, 0) // no entry ids
	} else {
		b = append(b, 1)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(entryIDs)))
		for _, eid := range entryIDs {
			b = binary.LittleEndian.AppendUint32(b, uint32(len(eid)))
			b = append(b, eid...)
		}
	}
	b = binary.LittleEndian.AppendUint32(b, 0) // cb_auxin
	return b
}

// TestNspiModLinkAttWritesOwnDelegateList drives Bind then ModLinkAtt over the
// MAPI/HTTP transport, proving the route is wired, the request decodes, the
// authenticated login arrives as the primary SMTP so the owner-only access check
// passes, and the write persists. Editing another user's list is then denied.
func TestNspiModLinkAttWritesOwnDelegateList(t *testing.T) {
	// alice@hermex.test (the login) and bob@hermex.test, both with mailboxes so
	// they appear in the GAL in address order: alice = midBase (0x10), bob = 0x11.
	accs := delegatingAccounts{
		StaticAccounts: directory.StaticAccounts{
			testUser:          {Password: testPass, MailboxPath: t.TempDir()},
			"bob@hermex.test": {Password: testPass, MailboxPath: t.TempDir()},
		},
		delegates: map[string][]string{},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test", nil).Handler())
	t.Cleanup(ts.Close)

	bind := mapiPost(t, ts, "/mapi/nspi", "Bind", bindBody(0), nil)
	bind.Body.Close()
	sid, seq := cookieByName(bind, "sid"), cookieByName(bind, "sequence")
	if sid == "" || seq == "" {
		t.Fatal("no cookies from Bind")
	}
	modLink := func(mid uint32, eid []byte) uint32 {
		resp := mapiPost(t, ts, "/mapi/nspi", "ModLinkAtt", modLinkAttBody(0, uint32(mapi.PrEmsAbPublicDelegates), mid, [][]byte{eid}), func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
			r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
		})
		defer resp.Body.Close()
		if got := resp.Header.Get("X-ResponseCode"); got != "0" {
			t.Fatalf("ModLinkAtt: X-ResponseCode = %q, want 0 (the op must be routed, not rejected)", got)
		}
		if ns := cookieByName(resp, "sequence"); ns != "" {
			seq = ns
		}
		p := nspiPayload(t, resp)
		if len(p) < 8 {
			t.Fatalf("ModLinkAtt response too short: %d bytes", len(p))
		}
		return binary.LittleEndian.Uint32(p[4:8]) // result
	}

	// alice (mid 0x10) adds bob (mid 0x11, ephemeral EID) to her own list.
	if result := modLink(0x10, ephemeralEID(0x11)); result != 0 {
		t.Fatalf("ModLinkAtt on own list = result %#x, want ecSuccess", result)
	}
	if got := accs.delegates["alice@hermex.test"]; len(got) != 1 || got[0] != "bob@hermex.test" {
		t.Fatalf("alice's delegates = %v, want [bob@hermex.test]", got)
	}

	// alice tries to edit bob's list (mid 0x11) — denied (0x80070005), no mutation.
	if result := modLink(0x11, ephemeralEID(0x10)); result != 0x80070005 {
		t.Errorf("ModLinkAtt on another user's list = result %#x, want ecAccessDenied", result)
	}
	if _, ok := accs.delegates["bob@hermex.test"]; ok {
		t.Error("a denied ModLinkAtt mutated bob's delegate list")
	}
}
