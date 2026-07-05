package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestContactRichFieldsRoundTrip proves the rich contact fields (mobile/home
// phones, birthday, home address, job title, department) survive a create-then-
// reload through the vCard path oxvcard owns, so the form is not a no-op after a
// refresh. The assertion is on the exact values, not just presence.
func TestContactRichFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("contacts-rich-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	body := `{"name":"Ada Lovelace","email":"ada@analytical.test","phone":"+1 555 0100","mobilePhone":"+1 555 0101","homePhone":"+1 555 0102","company":"Analytical Engine","jobTitle":"Mathematician","department":"Science","birthday":"1815-12-10","billing":"Project X-1815","homeStreet":"221B Baker St","homeCity":"London","homeState":"","homePostal":"NW1","homeCountry":"UK","imAddress":"ada@im.test","webPage":"https://example.test/ada"}`
	if rec := do(http.MethodPost, "/api/v1/contacts", body); rec.Code != http.StatusOK {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}

	rec := do(http.MethodGet, "/api/v1/contacts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	var listed struct {
		Contacts []contactJSON `json:"contacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Contacts) != 1 {
		t.Fatalf("got %d contacts, want 1", len(listed.Contacts))
	}
	c := listed.Contacts[0]
	checks := map[string]string{
		"name": c.Name, "email": c.Email, "phone": c.Phone, "mobilePhone": c.MobilePhone,
		"homePhone": c.HomePhone, "company": c.Company, "jobTitle": c.JobTitle,
		"department": c.Department, "birthday": c.Birthday, "billing": c.Billing,
		"homeStreet": c.HomeStreet, "homeCity": c.HomeCity, "homePostal": c.HomePostal,
		"homeCountry": c.HomeCountry, "imAddress": c.IMAddress, "webPage": c.WebPage,
	}
	want := map[string]string{
		"name": "Ada Lovelace", "email": "ada@analytical.test", "phone": "+1 555 0100",
		"mobilePhone": "+1 555 0101", "homePhone": "+1 555 0102", "company": "Analytical Engine",
		"jobTitle": "Mathematician", "department": "Science", "birthday": "1815-12-10",
		"billing": "Project X-1815", "homeStreet": "221B Baker St", "homeCity": "London",
		"homePostal": "NW1", "homeCountry": "UK", "imAddress": "ada@im.test",
		"webPage": "https://example.test/ada",
	}
	for k, w := range want {
		if checks[k] != w {
			t.Errorf("%s = %q, want %q", k, checks[k], w)
		}
	}
}
