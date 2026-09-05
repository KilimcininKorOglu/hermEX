package webmail2api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// appAuth is an sfAuth that also keeps app passwords in memory.
type appAuth struct {
	*sfAuth
	next  int64
	items map[int64]string // id -> secret
	names map[int64]string
}

func newAppAuth(base *sfAuth) *appAuth {
	return &appAuth{sfAuth: base, items: map[int64]string{}, names: map[int64]string{}}
}

func (a *appAuth) CreateAppPassword(_, name string) (string, error) {
	if len(a.items) >= directory.MaxAppPasswords {
		return "", directory.ErrTooManyAppPasswords
	}
	a.next++
	secret := "SECRET" + strconv.FormatInt(a.next, 10)
	a.items[a.next] = secret
	a.names[a.next] = name
	return secret, nil
}

func (a *appAuth) ListAppPasswords(string) ([]directory.AppPassword, error) {
	out := make([]directory.AppPassword, 0, len(a.items))
	for id := a.next; id >= 1; id-- {
		if _, ok := a.items[id]; ok {
			out = append(out, directory.AppPassword{ID: id, Name: a.names[id]})
		}
	}
	return out, nil
}

func (a *appAuth) DeleteAppPassword(_ string, id int64) (bool, error) {
	if _, ok := a.items[id]; !ok {
		return false, nil
	}
	delete(a.items, id)
	return true, nil
}

func (a *appAuth) AuthenticateAppPassword(string, string) (string, bool) { return "", false }

// appHarness returns the directory plus one browser, signed in.
func appHarness(t *testing.T) (*appAuth, requestFunc) {
	t.Helper()
	base, _ := sfHarness(t)
	auth := newAppAuth(base)
	do := browser(auth, auth.accounts)
	sfLogin(t, do)
	return auth, do
}

// TestMintingACredentialAsksForThePassword keeps a stolen session from quietly
// creating a way into the mailbox that survives the password being changed.
func TestMintingACredentialAsksForThePassword(t *testing.T) {
	auth, do := appHarness(t)

	if rec := do(http.MethodPost, "/api/v1/account/app-passwords",
		`{"name":"Phone","password":"wrong"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("mint with a wrong password = %d, want 401", rec.Code)
	}
	if len(auth.items) != 0 {
		t.Fatal("a credential was minted on a wrong password")
	}

	rec := do(http.MethodPost, "/api/v1/account/app-passwords", `{"name":"Phone","password":"pw"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mint = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Secret == "" {
		t.Error("the mint response carried no secret")
	}
}

// TestTheListNeverCarriesASecret is why the secret is shown once: the listing is
// read on every visit to the settings page, and a secret in it would be a
// credential handed back to any session that reaches the page.
func TestTheListNeverCarriesASecret(t *testing.T) {
	_, do := appHarness(t)
	if rec := do(http.MethodPost, "/api/v1/account/app-passwords", `{"name":"Phone","password":"pw"}`); rec.Code != http.StatusOK {
		t.Fatalf("mint = %d", rec.Code)
	}
	rec := do(http.MethodGet, "/api/v1/account/app-passwords", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"name":"Phone"`) {
		t.Fatalf("the listing does not describe the credential: %s", body)
	}
	if strings.Contains(body, "SECRET") {
		t.Errorf("the listing carried the secret: %s", body)
	}
}

// TestRevokingRemovesTheCredential covers the delete path and its bounds.
func TestRevokingRemovesTheCredential(t *testing.T) {
	auth, do := appHarness(t)
	if rec := do(http.MethodPost, "/api/v1/account/app-passwords", `{"name":"Phone","password":"pw"}`); rec.Code != http.StatusOK {
		t.Fatalf("mint = %d", rec.Code)
	}
	if rec := do(http.MethodDelete, "/api/v1/account/app-passwords/999", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoking an unknown id = %d, want 404", rec.Code)
	}
	if rec := do(http.MethodDelete, "/api/v1/account/app-passwords/1", ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", rec.Code, rec.Body.String())
	}
	if len(auth.items) != 0 {
		t.Error("the credential survived the revoke")
	}
}

// TestAPendingSessionCannotMintACredential closes the obvious way around the
// second factor: a half-finished login minting a credential that every protocol
// accepts without one.
func TestAPendingSessionCannotMintACredential(t *testing.T) {
	auth, do := appHarness(t)
	// Enroll, then start a fresh login that stops at the code prompt.
	auth.secret, auth.enabled = "JBSWY3DPEHPK3PXP", true
	sfLogin(t, do)
	for _, c := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/account/app-passwords", ""},
		{http.MethodPost, "/api/v1/account/app-passwords", `{"name":"Phone","password":"pw"}`},
		{http.MethodDelete, "/api/v1/account/app-passwords/1", ""},
	} {
		if rec := do(c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with a pending session = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if len(auth.items) != 0 {
		t.Error("a pending session minted a credential")
	}
}
