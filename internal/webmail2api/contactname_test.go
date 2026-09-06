package webmail2api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// contactHarness returns a signed-in browser against one mailbox.
func contactHarness(t *testing.T) requestFunc {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	auth := &sfAuth{accounts: directory.StaticAccounts{
		"alice@hermex.test": {Password: "pw", MailboxPath: dir},
	}}
	do := browser(auth, auth.accounts)
	sfLogin(t, do)
	return do
}

// contactNames lists the names the contacts endpoint reports.
func contactNames(t *testing.T, do requestFunc) []string {
	t.Helper()
	rec := do(http.MethodGet, "/api/v1/contacts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list contacts = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Contacts []struct {
			Name string `json:"name"`
		} `json:"contacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(out.Contacts))
	for _, c := range out.Contacts {
		names = append(names, c.Name)
	}
	return names
}

// TestANamelessContactIsRefused is the silent-success guard. A contact stored
// with no name is not rejected further down: vCard requires an FN, so the export
// substitutes a placeholder and the contact reads back as "Unknown" in every
// client. A caller that named the wrong field saw a saved contact and no error.
func TestANamelessContactIsRefused(t *testing.T) {
	do := contactHarness(t)

	rec := do(http.MethodPost, "/api/v1/contacts", `{"displayName":"Ada Lovelace"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a contact with no name and no address = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "name or an email") {
		t.Errorf("the refusal does not say what is missing: %s", rec.Body.String())
	}
	if names := contactNames(t, do); len(names) != 0 {
		t.Errorf("the refused contact was stored: %v", names)
	}
}

// TestAContactWithOnlyAnAddressIsFiledUnderIt keeps the ordinary case working: a
// mail client files a nameless address under the address itself, never under a
// placeholder.
func TestAContactWithOnlyAnAddressIsFiledUnderIt(t *testing.T) {
	do := contactHarness(t)

	if rec := do(http.MethodPost, "/api/v1/contacts", `{"email":"ada@partner.example"}`); rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	names := contactNames(t, do)
	if len(names) != 1 || names[0] != "ada@partner.example" {
		t.Errorf("names = %v, want the address", names)
	}
}

// TestAStructuredNameIsAssembled covers the client that sends the parts without
// the formatted name.
func TestAStructuredNameIsAssembled(t *testing.T) {
	do := contactHarness(t)

	if rec := do(http.MethodPost, "/api/v1/contacts",
		`{"firstName":"Ada","lastName":"Lovelace","email":"ada@partner.example"}`); rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	names := contactNames(t, do)
	if len(names) != 1 || names[0] != "Ada Lovelace" {
		t.Errorf("names = %v, want the assembled name", names)
	}
}

// TestANamelessGroupIsRefused covers the other shape: a contact group has no
// address to fall back to, so a name is the only thing that identifies it.
func TestANamelessGroupIsRefused(t *testing.T) {
	do := contactHarness(t)

	rec := do(http.MethodPost, "/api/v1/contacts", `{"is_group":true,"members":["a@b.test"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a nameless group = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestARefusedEditKeepsTheContact is why the check runs before the delete: an
// update replaces the object, so a rejection after the delete would remove a
// contact the caller was only trying to edit.
func TestARefusedEditKeepsTheContact(t *testing.T) {
	do := contactHarness(t)

	rec := do(http.MethodPost, "/api/v1/contacts", `{"name":"Ada Lovelace","email":"ada@partner.example"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d", rec.Code)
	}
	var created struct {
		Contact struct {
			ID string `json:"id"`
		} `json:"contact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	if rec := do(http.MethodPut, "/api/v1/contacts/"+created.Contact.ID, `{"phone":"+1 555 0100"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("a nameless edit = %d, want 400", rec.Code)
	}
	names := contactNames(t, do)
	if len(names) != 1 || names[0] != "Ada Lovelace" {
		t.Errorf("names = %v, want the untouched contact", names)
	}
}
