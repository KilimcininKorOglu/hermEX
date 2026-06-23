package ews

import (
	"encoding/base64"
	"strings"
	"testing"

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
