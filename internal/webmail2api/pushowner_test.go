package webmail2api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// takenPushStore refuses every save as an endpoint another account holds, and can
// fail a delete.
type takenPushStore struct {
	*recordingPushStore
	saveErr   error
	deleteErr error
}

func (p takenPushStore) SavePushSubscription(directory.PushSubscription) error { return p.saveErr }

func (p takenPushStore) DeletePushSubscription(endpoint string) error {
	if p.deleteErr != nil {
		return p.deleteErr
	}
	return p.recordingPushStore.DeletePushSubscription(endpoint)
}

// pushRequest sends one push request as alice against store and returns the status.
func pushRequest(t *testing.T, store takenPushStore, method, path, body string) int {
	t.Helper()
	mbox := t.TempDir()
	store.StaticAccounts = directory.StaticAccounts{"alice@hermex.test": {MailboxPath: mbox}}
	secret := []byte("push-owner-test-secret")
	srv := NewServer(store, store, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix(),
	})
	mustNoErr(t, "mint the session", err)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// TestPushSubscribeRefusesAnotherAccountsEndpoint proves an endpoint the directory
// keeps for another account answers 409 rather than a server error, and that a
// store failure is recorded.
func TestPushSubscribeRefusesAnotherAccountsEndpoint(t *testing.T) {
	sink := withSink(t)
	body := `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc123","keys":{"p256dh":"BPub","auth":"BAuth"}}`
	taken := takenPushStore{recordingPushStore: &recordingPushStore{}, saveErr: directory.ErrPushEndpointTaken}
	wantEq(t, "subscribe to a taken endpoint", pushRequest(t, taken, http.MethodPost, "/api/v1/push/subscribe", body), http.StatusConflict)

	broken := takenPushStore{recordingPushStore: &recordingPushStore{}, saveErr: errors.New("db down")}
	wantEq(t, "subscribe with a failing store", pushRequest(t, broken, http.MethodPost, "/api/v1/push/subscribe", body), http.StatusInternalServerError)
	if _, ok := sink.find("push-subscribe"); !ok {
		t.Error("the failed subscription save was not recorded")
	}
}

// TestPushUnsubscribeLeavesAnotherAccountsDevice: an endpoint is public to
// anyone who saw it on the wire, so naming bob's endpoint as alice must not
// remove bob's subscription.
func TestPushUnsubscribeLeavesAnotherAccountsDevice(t *testing.T) {
	const endpoint = "https://fcm.googleapis.com/fcm/send/bob-device"
	rec := &recordingPushStore{saved: []directory.PushSubscription{
		{Endpoint: endpoint, Email: "bob@hermex.test", P256dh: "k", Auth: "a"},
	}}
	pushRequest(t, takenPushStore{recordingPushStore: rec}, http.MethodDelete, "/api/v1/push/unsubscribe?endpoint="+url.QueryEscape(endpoint), "")
	wantEq(t, "bob's subscriptions after alice's unsubscribe", len(rec.saved), 1)
}

// TestPushUnsubscribeReportsAFailedDelete proves a delete that fails is answered as
// an error and recorded, rather than reported as done while the device keeps
// receiving notices.
func TestPushUnsubscribeReportsAFailedDelete(t *testing.T) {
	sink := withSink(t)
	const endpoint = "https://fcm.googleapis.com/fcm/send/abc123"
	store := takenPushStore{recordingPushStore: &recordingPushStore{saved: []directory.PushSubscription{
		{Endpoint: endpoint, Email: "alice@hermex.test", P256dh: "k", Auth: "a"},
	}}, deleteErr: errors.New("db down")}
	code := pushRequest(t, store, http.MethodDelete, "/api/v1/push/unsubscribe?endpoint="+url.QueryEscape(endpoint), "")
	wantEq(t, "unsubscribe with a failing delete", code, http.StatusInternalServerError)
	if _, ok := sink.find("push-unsubscribe"); !ok {
		t.Error("the failed delete was not recorded")
	}
}
