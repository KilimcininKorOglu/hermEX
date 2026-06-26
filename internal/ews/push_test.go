package ews

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// notificationResult builds the SOAP envelope a push callback returns, carrying the
// SubscriptionStatus (OK keeps the subscription, Unsubscribe stops it).
func notificationResult(status string) string {
	return `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"` +
		` xmlns:m="` + nsMessages + `"><soap:Body>` +
		`<m:SendNotificationResult><m:SubscriptionStatus>` + status + `</m:SubscriptionStatus>` +
		`</m:SendNotificationResult></soap:Body></soap:Envelope>`
}

// pushSubscribeInner builds a PushSubscriptionRequest watching the whole mailbox for
// CreatedEvent, with the given callback URL and a 1-minute StatusFrequency.
func pushSubscribeInner(callbackURL string) string {
	return `<Subscribe xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<t:PushSubscriptionRequest SubscribeToAllFolders="true">` +
		`<t:EventTypes><t:EventType>CreatedEvent</t:EventType></t:EventTypes>` +
		`<t:StatusFrequency>1</t:StatusFrequency>` +
		`<t:URL>` + callbackURL + `</t:URL>` +
		`</t:PushSubscriptionRequest></Subscribe>`
}

// TestPushSubscribeDelivers proves the push path end-to-end: a Subscribe with a
// PushSubscriptionRequest registers a worker; a delivery plus a relay wake makes the
// worker POST a SendNotification carrying the CreatedEvent to the client's callback
// — without waiting out the StatusFrequency. The callback runs on loopback, so the
// test allows internal callbacks (the SSRF IP block is exercised separately).
func TestPushSubscribeDelivers(t *testing.T) {
	srv, sess, path := subServer(t)
	srv.pushAllowInternal = true // the stub callback is on 127.0.0.1
	waker := &fakeStreamWaker{chans: map[string]chan struct{}{}}
	srv.waker = waker

	got := make(chan string, 4)
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- string(body)
		io.WriteString(w, notificationResult("OK"))
	}))
	defer cb.Close()

	subscribe(t, srv, sess, pushSubscribeInner(cb.URL+"/callback"))

	// Deliver a message and fire the wake; the worker must POST a SendNotification.
	seedInbox(t, path, "push me")
	waker.fire(path)

	select {
	case body := <-got:
		if !strings.Contains(body, "SendNotification") {
			t.Errorf("callback body is not a SendNotification: %s", body)
		}
		if !strings.Contains(body, "CreatedEvent") {
			t.Errorf("callback did not carry the CreatedEvent: %s", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the push worker did not POST to the callback within 3s")
	}
}

// TestPushUnsubscribeOnClientRequest proves a callback answering Unsubscribe stops
// the worker and drops the subscription.
func TestPushUnsubscribeOnClientRequest(t *testing.T) {
	srv, sess, path := subServer(t)
	srv.pushAllowInternal = true
	waker := &fakeStreamWaker{chans: map[string]chan struct{}{}}
	srv.waker = waker

	hit := make(chan struct{}, 4)
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, notificationResult("Unsubscribe"))
		select {
		case hit <- struct{}{}:
		default:
		}
	}))
	defer cb.Close()

	id := subscribe(t, srv, sess, pushSubscribeInner(cb.URL+"/cb"))
	seedInbox(t, path, "once")
	waker.fire(path)

	select {
	case <-hit:
	case <-time.After(3 * time.Second):
		t.Fatal("callback never hit")
	}

	// The worker should drop the subscription after the Unsubscribe answer.
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.subMu.Lock()
		_, present := srv.subs[id]
		srv.subMu.Unlock()
		if !present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscription not dropped after the client answered Unsubscribe")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestIsPublicIP locks the SSRF address block: loopback, link-local (incl. the cloud
// metadata address), private, unspecified, and multicast are refused; routable
// addresses are allowed.
func TestIsPublicIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", // loopback
		"169.254.169.254", "169.254.1.1", "fe80::1", // link-local incl. metadata
		"10.0.0.1", "172.16.0.1", "192.168.1.1", "fc00::1", // private
		"0.0.0.0", "::", // unspecified
		"224.0.0.1", // multicast
	}
	for _, s := range blocked {
		if isPublicIP(net.ParseIP(s)) {
			t.Errorf("isPublicIP(%s) = true, want false (SSRF block)", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "203.0.113.5", "2606:4700:4700::1111"} {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("isPublicIP(%s) = false, want true (routable)", s)
		}
	}
}

// TestValidateCallbackURL locks the first SSRF gate: only absolute http(s) URLs with
// a host, and http only when explicitly allowed.
func TestValidateCallbackURL(t *testing.T) {
	if err := validateCallbackURL("", false); err == nil {
		t.Error("empty URL must be rejected")
	}
	if err := validateCallbackURL("http://cb.test/x", false); err == nil {
		t.Error("http must be rejected when not allowed")
	}
	if err := validateCallbackURL("http://cb.test/x", true); err != nil {
		t.Errorf("http must be allowed with allowHTTP: %v", err)
	}
	if err := validateCallbackURL("ftp://cb.test/x", false); err == nil {
		t.Error("non-http scheme must be rejected")
	}
	if err := validateCallbackURL("https:///x", false); err == nil {
		t.Error("URL without a host must be rejected")
	}
	if err := validateCallbackURL("https://cb.test/x", false); err != nil {
		t.Errorf("a valid https URL must pass: %v", err)
	}
}

// TestDeliverPushRefusesInternal proves the dial-time guard refuses a callback that
// resolves to a non-public address when internal callbacks are not allowed.
func TestDeliverPushRefusesInternal(t *testing.T) {
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer cb.Close()
	srv := &Server{} // pushAllowInternal is false
	srv.ensurePushClient()
	if _, err := srv.deliverPush(cb.URL+"/cb", []byte("<x/>")); err == nil {
		t.Error("deliverPush to a loopback callback must be refused by the SSRF guard")
	}
}
