package webmail2api

import (
	"encoding/json"
	"net/http"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// sharedSendGrant reads the send grant /mailboxes/shared reports for team.
func sharedSendGrant(t *testing.T, f *sharedMessageFixture) string {
	t.Helper()
	rec := f.do(http.MethodGet, "/api/v1/mailboxes/shared", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Shared []struct {
			Owner     string `json:"owner"`
			SendGrant string `json:"sendGrant"`
		} `json:"shared_mailboxes"`
	}
	mustNoErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &body))
	if len(body.Shared) != 1 || body.Shared[0].Owner != "team@hermex.test" {
		t.Fatalf("shared mailboxes = %+v, want team alone", body.Shared)
	}
	return body.Shared[0].SendGrant
}

// TestSharedMailboxReportsItsSendGrant proves the shared-mailbox list tells the
// compose picker which From the caller may use for each mailbox. It used to list
// every mailbox the caller could open as sendable, so a reviewer without a send
// grant was offered an identity the send gate then refused.
func TestSharedMailboxReportsItsSendGrant(t *testing.T) {
	f := newSharedMessageFixture(t, mapi.RightsReviewer, false)
	if g := sharedSendGrant(t, f); g != "none" {
		t.Errorf("without a grant: sendGrant = %q, want none", g)
	}
	st, err := objectstore.Open(f.shared)
	mustNoErr(t, "open shared", err)
	mustNoErr(t, "on-behalf", st.SetSendOnBehalf([]string{"alice@hermex.test"}))
	mustNoErr(t, "close", st.Close())
	if g := sharedSendGrant(t, f); g != "on-behalf" {
		t.Errorf("with send-on-behalf: sendGrant = %q, want on-behalf", g)
	}
	st, err = objectstore.Open(f.shared)
	mustNoErr(t, "open shared", err)
	mustNoErr(t, "send-as", st.SetSendAs([]string{"alice@hermex.test"}))
	mustNoErr(t, "close", st.Close())
	if g := sharedSendGrant(t, f); g != "send-as" {
		t.Errorf("with send-as: sendGrant = %q, want send-as", g)
	}
}
