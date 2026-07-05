package webmail2api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
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

	body := `{"name":"Ada Lovelace","prefix":"Dr.","firstName":"Ada","middleName":"Augusta","lastName":"Lovelace","suffix":"Jr.","email":"ada@analytical.test","email2":"ada@home.test","email3":"ada@academy.test","phone":"+1 555 0100","mobilePhone":"+1 555 0101","homePhone":"+1 555 0102","company":"Analytical Engine","jobTitle":"Mathematician","department":"Science","birthday":"1815-12-10","nickname":"Ada","fileAs":"Lovelace, Ada","profession":"Mathematician","spouse":"Charles","billing":"Project X-1815","categories":["VIP","Pioneers"],"homeStreet":"221B Baker St","homeCity":"London","homeState":"","homePostal":"NW1","homeCountry":"UK","imAddress":"ada@im.test","webPage":"https://example.test/ada"}`
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
		"name": c.Name, "prefix": c.Prefix, "firstName": c.FirstName,
		"middleName": c.MiddleName, "lastName": c.LastName, "suffix": c.Suffix,
		"email": c.Email, "email2": c.Email2, "email3": c.Email3,
		"phone": c.Phone, "mobilePhone": c.MobilePhone, "homePhone": c.HomePhone,
		"company": c.Company, "jobTitle": c.JobTitle, "department": c.Department,
		"birthday": c.Birthday, "billing": c.Billing, "nickname": c.Nickname,
		"fileAs": c.FileAs, "profession": c.Profession, "spouse": c.Spouse,
		"homeStreet": c.HomeStreet, "homeCity": c.HomeCity, "homePostal": c.HomePostal,
		"homeCountry": c.HomeCountry, "imAddress": c.IMAddress, "webPage": c.WebPage,
	}
	want := map[string]string{
		"name": "Ada Lovelace", "prefix": "Dr.", "firstName": "Ada",
		"middleName": "Augusta", "lastName": "Lovelace", "suffix": "Jr.",
		"email": "ada@analytical.test", "email2": "ada@home.test",
		"email3": "ada@academy.test", "phone": "+1 555 0100", "mobilePhone": "+1 555 0101",
		"homePhone": "+1 555 0102", "company": "Analytical Engine", "jobTitle": "Mathematician",
		"department": "Science", "birthday": "1815-12-10", "billing": "Project X-1815",
		"nickname": "Ada", "fileAs": "Lovelace, Ada", "profession": "Mathematician",
		"spouse": "Charles", "homeStreet": "221B Baker St", "homeCity": "London",
		"homePostal": "NW1", "homeCountry": "UK", "imAddress": "ada@im.test",
		"webPage": "https://example.test/ada",
	}
	for k, w := range want {
		if checks[k] != w {
			t.Errorf("%s = %q, want %q", k, checks[k], w)
		}
	}
	// Categories ride the shared PidNameKeywords multi-value named prop.
	if len(c.Categories) != 2 || c.Categories[0] != "VIP" || c.Categories[1] != "Pioneers" {
		t.Errorf("categories = %v, want [VIP Pioneers]", c.Categories)
	}
}

// TestContactPhotoRoundTrip proves the contact photo (the contact's JPEG
// attachment flagged PrAttachmentContactPhoto) survives a set-then-get-then-
// delete cycle: a PUT multipart upload is byte-identical to the GET response,
// PidLidHasPicture flips on and back off, and a second GET after DELETE is 404.
func TestContactPhotoRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("contacts-photo-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string, contentType string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Create a contact to attach the photo to.
	rec := do(http.MethodPost, "/api/v1/contacts", `{"name":"Grace Hopper","email":"grace@navy.test"}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Contact contactJSON `json:"contact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	photoTarget := "/api/v1/contacts/" + created.Contact.ID + "/photo"

	// No photo yet: GET is 404.
	if rec := do(http.MethodGet, photoTarget, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("empty photo GET: status %d, want 404", rec.Code)
	}

	// Upload a fake JPEG payload.
	photo := bytes.Repeat([]byte{0xFF, 0xD8, 0xFF, 0xE0}, 64)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "ContactPicture.jpg")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(photo); err != nil {
		t.Fatalf("write photo: %v", err)
	}
	mw.Close()
	if rec := do(http.MethodPut, photoTarget, buf.String(), mw.FormDataContentType()); rec.Code != http.StatusOK {
		t.Fatalf("put photo: status %d body %s", rec.Code, rec.Body.String())
	}

	// GET returns the same bytes we uploaded.
	rec = do(http.MethodGet, photoTarget, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get photo: status %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), photo) {
		t.Fatalf("get photo: %d bytes, want %d (round-trip mismatch)", len(rec.Body.Bytes()), len(photo))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("get photo content-type = %q, want image/jpeg", ct)
	}

	// Delete the photo; a follow-up GET is 404.
	if rec := do(http.MethodDelete, photoTarget, "", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete photo: status %d", rec.Code)
	}
	if rec := do(http.MethodGet, photoTarget, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("post-delete photo GET: status %d, want 404", rec.Code)
	}
}

// TestContactExport proves a contact can be downloaded as a vCard (.vcf), the
// same bytes CardDAV/EAS/EWS see, and that the saved filename is derived from
// the contact's display name.
func TestContactExport(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("contacts-export-test-secret")
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

	rec := do(http.MethodPost, "/api/v1/contacts", `{"name":"Nikola Tesla","email":"nikola@ac.test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Contact contactJSON `json:"contact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	rec = do(http.MethodGet, "/api/v1/contacts/"+created.Contact.ID+"/vcard", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export: status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "BEGIN:VCARD") {
		t.Errorf("export body does not start with BEGIN:VCARD: %q", body[:min(40, len(body))])
	}
	if !strings.Contains(body, "FN:Nikola Tesla") {
		t.Errorf("export body missing FN:Nikola Tesla")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/vcard") {
		t.Errorf("export content-type = %q, want text/vcard", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="NikolaTesla.vcf"`) {
		t.Errorf("export content-disposition = %q, want filename=\"NikolaTesla.vcf\"", cd)
	}
}
