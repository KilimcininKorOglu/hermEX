package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// hidingAccounts is a directory whose GAL carries the operator's hide mask, which
// the static directory never sets. It records the hide bits each search asked the
// directory for.
type hidingAccounts struct {
	directory.StaticAccounts
	hidden map[string]uint32
	asked  *[]uint32
}

func (h hidingAccounts) SearchGAL(caller, q string, limit int, hide uint32) ([]directory.GALEntry, error) {
	*h.asked = append(*h.asked, hide)
	entries, err := h.StaticAccounts.SearchGAL(caller, q, limit, hide)
	for i := range entries {
		entries[i].HiddenFrom = h.hidden[entries[i].Address]
	}
	return entries, err
}

// TestDirectoryOmitsHiddenUser proves the webmail directory endpoint, which backs
// both the address-book page and recipient completion, does not return a user the
// operator hid from the address book.
func TestDirectoryOmitsHiddenUser(t *testing.T) {
	mbox := t.TempDir()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	accs := hidingAccounts{
		StaticAccounts: directory.StaticAccounts{
			"alice@hermex.test": {MailboxPath: mbox},
			"alex@hermex.test":  {MailboxPath: t.TempDir()},
		},
		hidden: map[string]uint32{"alex@hermex.test": directory.HideFromGAL},
		asked:  &[]uint32{},
	}
	secret := []byte("directory-hide-test-secret")
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)

	token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/directory?q=al", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("directory search = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "alex@hermex.test") {
		t.Errorf("the directory returned a user hidden from the address book: %s", body)
	}
	if !strings.Contains(body, "alice@hermex.test") {
		t.Errorf("the directory dropped the visible user: %s", body)
	}
	// The directory must drop hidden entries before its row limit, or they use
	// up the answer and push visible users out of it.
	if got := *accs.asked; len(got) != 1 || got[0] != directory.HideFromGAL {
		t.Errorf("hide bits asked of the directory = %v, want [%d]", got, directory.HideFromGAL)
	}
}
