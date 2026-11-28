package ews

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPushSubscribeRefusalHidesResolvedAddress is the disclosure defect. The
// callback rejection names the address the callback resolved to, so returning the
// guard's own error text turned this endpoint into a reconnaissance oracle: a
// subscriber could submit a hostname and read back whether it points inside the
// network and at which private address, which is precisely what the guard is
// there to hide.
func TestPushSubscribeRefusalHidesResolvedAddress(t *testing.T) {
	srv, sess, _ := subServer(t)
	srv.pushAllowInternal = false

	for _, url := range []string{"https://127.0.0.1:9/cb", "http://example.com/cb", ""} {
		var req pushSubscriptionReq
		req.URL = url
		req.SubscribeToAllFolders = true

		rec := httptest.NewRecorder()
		srv.handlePushSubscribe(rec, &req, sess)

		wantEq(t, "fault status for "+url, rec.Code, http.StatusInternalServerError)
		body := rec.Body.String()
		if !strings.Contains(body, "ErrorInvalidSubscriptionRequest") {
			t.Fatalf("subscribe with refused callback %q did not fault: %s", url, body)
		}
		if strings.Contains(body, "127.0.0.1") {
			t.Errorf("the fault disclosed the resolved address:\n%s", body)
		}
		if strings.Contains(body, "ssrfguard") || strings.Contains(body, "not public") {
			t.Errorf("the fault leaked the guard's internal error text:\n%s", body)
		}
	}
	srv.subMu.Lock()
	subsCount := len(srv.subs)
	srv.subMu.Unlock()
	wantEq(t, "subscriptions registered after refusals", subsCount, 0)
}
