package webmail2api

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hermex/internal/directory"
)

// recordingPushStore is a directory that also holds push subscriptions, so a test can
// assert not just the response but whether a rejected endpoint was stored.
type recordingPushStore struct {
	directory.StaticAccounts
	saved []directory.PushSubscription
}

func (p *recordingPushStore) SavePushSubscription(sub directory.PushSubscription) error {
	p.saved = append(p.saved, sub)
	return nil
}

func (p *recordingPushStore) ListPushSubscriptions(email string) ([]directory.PushSubscription, error) {
	var out []directory.PushSubscription
	for _, s := range p.saved {
		if s.Email == email {
			out = append(out, s)
		}
	}
	return out, nil
}

func (p *recordingPushStore) DeletePushSubscription(endpoint string) error {
	for i, s := range p.saved {
		if s.Endpoint == endpoint {
			p.saved = append(p.saved[:i], p.saved[i+1:]...)
			break
		}
	}
	return nil
}

func (p *recordingPushStore) PushSubscriberEmails() ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, s := range p.saved {
		if !seen[s.Email] {
			seen[s.Email] = true
			out = append(out, s.Email)
		}
	}
	return out, nil
}

// subscribeWith posts a push subscription for the given endpoint and reports the
// status code plus the store, so a test can check both halves.
func subscribeWith(t *testing.T, endpoint string) (int, *recordingPushStore) {
	t.Helper()
	mbox := t.TempDir()
	store := &recordingPushStore{StaticAccounts: directory.StaticAccounts{"alice@hermex.test": {MailboxPath: mbox}}}
	secret := []byte("push-ssrf-test-secret")
	srv := NewServer(store, store, nil, "mail.hermex.test", secret, "", false)

	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"endpoint":"` + endpoint + `","keys":{"p256dh":"BPub","auth":"BAuth"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push/subscribe", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, store
}

// TestSubscribeRefusesInternalEndpoint proves an endpoint pointing into the private
// address space is refused and, more to the point, never stored. Storing it would
// leave the poller dialing an internal service on its own schedule, with the caller
// choosing both the destination and (by receiving mail) the cadence.
func TestSubscribeRefusesInternalEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"https://127.0.0.1:8081/push",
		"https://169.254.169.254/latest/meta-data/",
		"https://10.0.0.5/push",
	} {
		t.Run(endpoint, func(t *testing.T) {
			code, store := subscribeWith(t, endpoint)
			if code != http.StatusBadRequest {
				t.Errorf("subscribe = %d, want 400", code)
			}
			if len(store.saved) != 0 {
				t.Errorf("the refused endpoint was stored anyway: %+v", store.saved)
			}
		})
	}
}

// TestSubscribeRefusesNonHTTPSEndpoint proves the scheme gate holds: plaintext and
// non-http schemes are refused, so a subscription cannot name something the guarded
// client was never built to dial.
func TestSubscribeRefusesNonHTTPSEndpoint(t *testing.T) {
	for _, endpoint := range []string{"http://push.example.test/x", "file:///etc/passwd", "gopher://x/1"} {
		t.Run(endpoint, func(t *testing.T) {
			code, store := subscribeWith(t, endpoint)
			if code != http.StatusBadRequest {
				t.Errorf("subscribe = %d, want 400", code)
			}
			if len(store.saved) != 0 {
				t.Errorf("the refused endpoint was stored anyway: %+v", store.saved)
			}
		})
	}
}

// TestSubscribeAcceptsPublicEndpoint is the control: a real push service endpoint
// still subscribes, so the gate did not simply break web push.
func TestSubscribeAcceptsPublicEndpoint(t *testing.T) {
	code, store := subscribeWith(t, "https://fcm.googleapis.com/fcm/send/abc123")
	if code != http.StatusOK {
		t.Fatalf("subscribe = %d, want 200", code)
	}
	if len(store.saved) != 1 || store.saved[0].Endpoint != "https://fcm.googleapis.com/fcm/send/abc123" {
		t.Errorf("the accepted endpoint was not stored: %+v", store.saved)
	}
}

// subscriptionKeys returns a real P-256 public point and auth secret, the shapes the
// web-push library needs to encrypt a payload. Placeholder strings would fail
// encryption before any request is made, which would make a delivery test vacuous.
func subscriptionKeys(t *testing.T) (p256dh, auth string) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	return base64.URLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		base64.URLEncoding.EncodeToString(secret)
}

// TestSendPushRefusesAStoredInternalEndpoint proves the dial-time guard is what
// actually protects the poller. An endpoint that bypassed the subscribe gate (a row
// written before this fix, or edited straight into the database) is still refused
// when the poller tries to deliver to it.
func TestSendPushRefusesAStoredInternalEndpoint(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer target.Close()

	p256dh, auth := subscriptionKeys(t)
	mbox := t.TempDir()
	store := &recordingPushStore{
		StaticAccounts: directory.StaticAccounts{"alice@hermex.test": {MailboxPath: mbox}},
		saved: []directory.PushSubscription{{
			Endpoint: target.URL + "/push", Email: "alice@hermex.test",
			P256dh: p256dh, Auth: auth,
		}},
	}
	srv := NewServer(store, store, nil, "mail.hermex.test", []byte("push-ssrf-test-secret"), "", false)

	// Control: with the guard relaxed the same delivery reaches the server, which
	// proves the keys and payload are good and the refusal below is the guard.
	srv.pushAllowInternal = true
	srv.sendPush(store.saved[0], []byte(`{"title":"x"}`))
	if hits.Load() == 0 {
		t.Fatal("the control delivery never reached the target; the test cannot prove anything")
	}

	guarded := NewServer(store, store, nil, "mail.hermex.test", []byte("push-ssrf-test-secret"), "", false)
	before := hits.Load()
	guarded.sendPush(store.saved[0], []byte(`{"title":"x"}`))

	if got := hits.Load(); got != before {
		t.Errorf("the loopback endpoint was contacted again (%d -> %d); the dial guard did not hold", before, got)
	}
	if len(store.saved) != 1 {
		t.Errorf("a guard refusal must not delete the subscription: %+v", store.saved)
	}
}
