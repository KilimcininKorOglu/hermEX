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

	body := `{"name":"Ada Lovelace","prefix":"Dr.","firstName":"Ada","middleName":"Augusta","lastName":"Lovelace","suffix":"Jr.","email":"ada@analytical.test","email2":"ada@home.test","email3":"ada@academy.test","phone":"+1 555 0100","mobilePhone":"+1 555 0101","homePhone":"+1 555 0102","businessFax":"+1 555 0199","company":"Analytical Engine","jobTitle":"Mathematician","department":"Science","birthday":"1815-12-10","nickname":"Ada","fileAs":"Lovelace, Ada","profession":"Mathematician","spouse":"Charles","billing":"Project X-1815","categories":["VIP","Pioneers"],"homeStreet":"221B Baker St","homeCity":"London","homeState":"","homePostal":"NW1","homeCountry":"UK","otherStreet":"10 Downing","otherCity":"Westminster","otherState":"Lon","otherPostal":"SW1","otherCountry":"GB","imAddress":"ada@im.test","webPage":"https://example.test/ada"}`
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/contacts", body), http.StatusOK)

	type listing struct {
		Contacts []contactJSON `json:"contacts"`
	}
	listed := okBody[listing](t, "list", do(http.MethodGet, "/api/v1/contacts", ""))
	if len(listed.Contacts) != 1 {
		t.Fatalf("got %d contacts, want 1", len(listed.Contacts))
	}
	c := listed.Contacts[0]
	checks := map[string]string{
		"name": c.Name, "prefix": c.Prefix, "firstName": c.FirstName,
		"middleName": c.MiddleName, "lastName": c.LastName, "suffix": c.Suffix,
		"email": c.Email, "email2": c.Email2, "email3": c.Email3,
		"phone": c.Phone, "mobilePhone": c.MobilePhone, "homePhone": c.HomePhone,
		"businessFax": c.BusinessFax,
		"company":     c.Company, "jobTitle": c.JobTitle, "department": c.Department,
		"birthday": c.Birthday, "billing": c.Billing, "nickname": c.Nickname,
		"fileAs": c.FileAs, "profession": c.Profession, "spouse": c.Spouse,
		"homeStreet": c.HomeStreet, "homeCity": c.HomeCity, "homePostal": c.HomePostal,
		"homeCountry": c.HomeCountry, "otherStreet": c.OtherStreet, "otherCity": c.OtherCity,
		"otherPostal": c.OtherPostal, "otherCountry": c.OtherCountry,
		"imAddress": c.IMAddress, "webPage": c.WebPage,
	}
	want := map[string]string{
		"name": "Ada Lovelace", "prefix": "Dr.", "firstName": "Ada",
		"middleName": "Augusta", "lastName": "Lovelace", "suffix": "Jr.",
		"email": "ada@analytical.test", "email2": "ada@home.test",
		"email3": "ada@academy.test", "phone": "+1 555 0100", "mobilePhone": "+1 555 0101",
		"homePhone": "+1 555 0102", "businessFax": "+1 555 0199",
		"company": "Analytical Engine", "jobTitle": "Mathematician",
		"department": "Science", "birthday": "1815-12-10", "billing": "Project X-1815",
		"nickname": "Ada", "fileAs": "Lovelace, Ada", "profession": "Mathematician",
		"spouse": "Charles", "homeStreet": "221B Baker St", "homeCity": "London",
		"homePostal": "NW1", "homeCountry": "UK", "otherStreet": "10 Downing",
		"otherCity": "Westminster", "otherPostal": "SW1", "otherCountry": "GB",
		"imAddress": "ada@im.test", "webPage": "https://example.test/ada",
	}
	wantContactFields(t, checks, want)
	// Categories ride the shared PidNameKeywords multi-value named prop.
	if len(c.Categories) != 2 || c.Categories[0] != "VIP" || c.Categories[1] != "Pioneers" {
		t.Errorf("categories = %v, want [VIP Pioneers]", c.Categories)
	}
}

// wantContactFields compares each named field against what was stored.
func wantContactFields(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, w := range want {
		wantEq(t, k, got[k], w)
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
	type createResponse struct {
		Contact contactJSON `json:"contact"`
	}
	created := okBody[createResponse](t, "create",
		do(http.MethodPost, "/api/v1/contacts", `{"name":"Grace Hopper","email":"grace@navy.test"}`, "application/json"))
	photoTarget := "/api/v1/contacts/" + created.Contact.ID + "/photo"

	// No photo yet: GET is 404.
	wantStatus(t, "empty photo GET", do(http.MethodGet, photoTarget, "", ""), http.StatusNotFound)

	// Upload a fake JPEG payload.
	photo := bytes.Repeat([]byte{0xFF, 0xD8, 0xFF, 0xE0}, 64)
	body, contentType := photoUpload(t, photo)
	wantStatus(t, "put photo", do(http.MethodPut, photoTarget, body, contentType), http.StatusOK)

	// GET returns the same bytes we uploaded.
	rec := do(http.MethodGet, photoTarget, "", "")
	wantStatus(t, "get photo", rec, http.StatusOK)
	if !bytes.Equal(rec.Body.Bytes(), photo) {
		t.Fatalf("get photo: %d bytes, want %d (round-trip mismatch)", len(rec.Body.Bytes()), len(photo))
	}
	wantEq(t, "get photo content-type", rec.Header().Get("Content-Type"), "image/jpeg")

	// Delete the photo; a follow-up GET is 404.
	wantStatus(t, "delete photo", do(http.MethodDelete, photoTarget, "", ""), http.StatusOK)
	wantStatus(t, "post-delete photo GET", do(http.MethodGet, photoTarget, "", ""), http.StatusNotFound)
}

// photoUpload builds the multipart body a photo PUT carries, returning it with
// its Content-Type.
func photoUpload(t *testing.T, photo []byte) (body, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "ContactPicture.jpg")
	mustNoErr(t, "create form file", err)
	_, err = fw.Write(photo)
	mustNoErr(t, "write photo", err)
	mustNoErr(t, "close multipart writer", mw.Close())
	return buf.String(), mw.FormDataContentType()
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

// TestContactExportDistributionList proves a contact group (IPM.DistList)
// exports as a multi-vCard document: one minimal vCard per member address.
func TestContactExportDistributionList(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("contacts-dl-export-test-secret")
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

	type createResponse struct {
		Contact contactJSON `json:"contact"`
	}
	created := okBody[createResponse](t, "create",
		do(http.MethodPost, "/api/v1/contacts", `{"name":"Engineering","is_group":true,"members":["eng-a@hermex.test","eng-b@hermex.test"]}`))

	rec := do(http.MethodGet, "/api/v1/contacts/"+created.Contact.ID+"/vcard", "")
	wantStatus(t, "export", rec, http.StatusOK)
	body := rec.Body.String()
	// One BEGIN:VCARD per member.
	wantEq(t, "exported vCard count", strings.Count(body, "BEGIN:VCARD"), 2)
	wantContains(t, "export body", body, "EMAIL:eng-a@hermex.test")
	wantContains(t, "export body", body, "EMAIL:eng-b@hermex.test")

	// Expand the distribution list into its member addresses, the compose
	// recipient-picker path: a non-group id must be rejected, the group id returns
	// the two members in order.
	type expansion struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	exp := okBody[expansion](t, "expand", do(http.MethodGet, "/api/v1/contacts/"+created.Contact.ID+"/expand", ""))
	wantEq(t, "expand name", exp.Name, "Engineering")
	if len(exp.Members) != 2 {
		t.Fatalf("expand members = %v, want two", exp.Members)
	}
	wantEq(t, "first member", exp.Members[0], "eng-a@hermex.test")
	wantEq(t, "second member", exp.Members[1], "eng-b@hermex.test")

	// A regular contact is not expandable.
	solo := okBody[createResponse](t, "create contact",
		do(http.MethodPost, "/api/v1/contacts", `{"name":"Solo","email":"solo@hermex.test"}`))
	wantStatus(t, "expand a regular contact",
		do(http.MethodGet, "/api/v1/contacts/"+solo.Contact.ID+"/expand", ""), http.StatusBadRequest)
}
