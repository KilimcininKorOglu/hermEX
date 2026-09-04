package ews

import (
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

	var req pushSubscriptionReq
	req.URL = "https://127.0.0.1:9/cb" // passes the scheme check, fails the address check
	req.SubscribeToAllFolders = true

	rec := httptest.NewRecorder()
	srv.handlePushSubscribe(rec, &req, sess)

	body := rec.Body.String()
	if !strings.Contains(body, "ErrorInvalidSubscriptionRequest") {
		t.Fatalf("subscribe with a refused callback did not fault: %s", body)
	}
	if strings.Contains(body, "127.0.0.1") {
		t.Errorf("the fault disclosed the resolved address:\n%s", body)
	}
	if strings.Contains(body, "ssrfguard") || strings.Contains(body, "not public") {
		t.Errorf("the fault leaked the guard's internal error text:\n%s", body)
	}
}
