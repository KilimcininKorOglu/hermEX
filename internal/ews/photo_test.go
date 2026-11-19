package ews

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestGetUserPhoto proves GetUserPhoto serves the caller's portrait from the
// cross-protocol photo property as base64 PictureData.
func TestGetUserPhoto(t *testing.T) {
	ts, dir := seededEWS(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	photo := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3, 4}
	if err := st.SetUserPhoto(photo); err != nil {
		t.Fatalf("set photo: %v", err)
	}
	st.Close()

	body := wrapRequest(`<GetUserPhoto xmlns="` + nsMessages + `"><Email>` + testUser +
		`</Email><SizeRequested>HR648x648</SizeRequested></GetUserPhoto>`)
	resp, out := soapPost(t, ts, body, true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("not a success: %s", out)
	}
	if want := base64.StdEncoding.EncodeToString(photo); !strings.Contains(out, want) {
		t.Errorf("PictureData missing the photo bytes: %s", out)
	}
}

// TestGetUserPhotoOfAHiddenUserIsNotServed asks for the portrait of a colleague
// the operator hid from the address book. Serving it would confirm a mailbox the
// directory hides, so the answer must be the same ErrorItemNotFound a missing
// photo gets; the same colleague left visible is served, so the refusal comes from
// the hide mask and not from a broken lookup.
func TestGetUserPhotoOfAHiddenUserIsNotServed(t *testing.T) {
	photo := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 9, 9}
	ask := func(mask uint32) string {
		alexDir := provisionedDir(t)
		st, err := objectstore.Open(alexDir)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetUserPhoto(photo); err != nil {
			t.Fatal(err)
		}
		st.Close()
		accs := hidingAccounts{
			StaticAccounts: directory.StaticAccounts{
				testUser:           {Password: testPass, MailboxPath: provisionedDir(t)},
				"alex@hermex.test": {Password: testPass, MailboxPath: alexDir},
			},
			hidden: map[string]uint32{"alex@hermex.test": mask},
			asked:  &[]uint32{},
		}
		ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
		t.Cleanup(ts.Close)
		_, out := soapPost(t, ts, wrapRequest(`<GetUserPhoto xmlns="`+nsMessages+
			`"><Email>alex@hermex.test</Email></GetUserPhoto>`), true)
		return out
	}
	want := base64.StdEncoding.EncodeToString(photo)
	if out := ask(0); !strings.Contains(out, want) {
		t.Fatalf("a visible colleague's portrait was not served: %s", out)
	}
	if out := ask(directory.HideFromGAL); strings.Contains(out, want) || !strings.Contains(out, "ErrorItemNotFound") {
		t.Errorf("a hidden colleague's portrait answer = %s, want ErrorItemNotFound without the photo", out)
	}
}

// TestGetUserPhotoNone proves a mailbox with no portrait answers
// ErrorItemNotFound rather than an empty success.
func TestGetUserPhotoNone(t *testing.T) {
	ts, _ := seededEWS(t)
	body := wrapRequest(`<GetUserPhoto xmlns="` + nsMessages + `"><Email>` + testUser + `</Email></GetUserPhoto>`)
	resp, out := soapPost(t, ts, body, true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "ErrorItemNotFound") {
		t.Errorf("expected ErrorItemNotFound: %s", out)
	}
}
