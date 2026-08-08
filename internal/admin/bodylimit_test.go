package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestLoginBodyIsBounded proves the cap covers the one route reachable with no
// credentials at all. Without it an unauthenticated client streams bytes into
// json.Decode on the daemon that manages every domain and mailbox on the instance.
func TestLoginBodyIsBounded(t *testing.T) {
	// The directory accepts these credentials and grants a system role, so an
	// unbounded decode would read the whole body and answer 200. Anything else
	// means the read was cut short, which is the point.
	ts := adminServer(t, &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}})

	body := `{"login":"admin@hermex.test","password":"pw","pad":"` + strings.Repeat("a", maxRequestBody+1024) + `"}`
	resp, err := http.Post(ts.URL+"/admin/login", "application/json", strings.NewReader(body))
	if err != nil {
		// A server-side MaxBytesReader trip can also surface as a broken pipe on
		// the client while it is still writing, which is the same refusal.
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusOK {
		t.Error("oversized login body was read to completion; the cap did not apply")
	}
}

// TestNormalLoginBodyStillWorks confirms the cap is nowhere near a real request:
// an ordinary login must still reach the credential check.
func TestNormalLoginBodyStillWorks(t *testing.T) {
	ts := adminServer(t, &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}})
	resp, _ := login(t, ts)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("an ordinary login body was rejected: status %d", resp.StatusCode)
	}
}
