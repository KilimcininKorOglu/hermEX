package webmail2api

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// pushStore is the optional directory capability for web-push subscriptions.
// SQLDirectory implements it; absent (static accounts) disables web push.
type pushStore interface {
	SavePushSubscription(directory.PushSubscription) error
	ListPushSubscriptions(email string) ([]directory.PushSubscription, error)
	DeletePushSubscription(endpoint string) error
	PushSubscriberEmails() ([]string, error)
}

// vapidKeys derives a stable VAPID P-256 keypair from the webmail secret, so every
// instance sharing the secret signs with the same key and the keys survive restarts
// with no separate storage. The values are base64url: the public key is the
// uncompressed EC point, the private key the scalar, as the web-push library wants.
// A derived 32-byte seed is almost always a valid P-256 scalar; the counter retries
// the negligibly rare case where it is out of range.
func vapidKeys(secret []byte) (public, private string, err error) {
	for i := range byte(16) {
		h := sha256.New()
		h.Write([]byte("webmail2-vapid-p256"))
		h.Write([]byte{i})
		h.Write(secret)
		priv, e := ecdh.P256().NewPrivateKey(h.Sum(nil))
		if e == nil {
			return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
				base64.RawURLEncoding.EncodeToString(priv.Bytes()), nil
		}
	}
	return "", "", errors.New("could not derive a vapid key from the secret")
}

// handlePushVapidKey returns the server's VAPID public key for PushManager.subscribe.
func (s *Server) handlePushVapidKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	pub, _, err := vapidKeys(s.secret)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "push not configured"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": pub})
}

// handlePushSubscribe stores a browser's push subscription for the caller.
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	store, ok := s.auth.(pushStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	var body struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Endpoint == "" || body.Keys.P256dh == "" || body.Keys.Auth == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad subscription"})
		return
	}
	if err := store.SavePushSubscription(directory.PushSubscription{
		Endpoint:  body.Endpoint,
		Email:     c.Email,
		P256dh:    body.Keys.P256dh,
		Auth:      body.Keys.Auth,
		CreatedAt: time.Now().Unix(),
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save subscription"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePushUnsubscribe removes a subscription by endpoint. It is scoped to the
// caller: the endpoint must belong to one of the caller's own subscriptions, so a
// known endpoint cannot be used to unsubscribe another user's device.
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	store, ok := s.auth.(pushStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	endpoint := r.URL.Query().Get("endpoint")
	if endpoint == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint required"})
		return
	}
	subs, err := store.ListPushSubscriptions(c.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not unsubscribe"})
		return
	}
	for _, sub := range subs {
		if sub.Endpoint == endpoint {
			_ = store.DeletePushSubscription(endpoint)
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePushSubscriptions lists the caller's own push subscriptions.
func (s *Server) handlePushSubscriptions(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	type keysJSON struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	}
	type subJSON struct {
		Endpoint string   `json:"endpoint"`
		Keys     keysJSON `json:"keys"`
	}
	out := make([]subJSON, 0)
	if store, ok := s.auth.(pushStore); ok {
		subs, err := store.ListPushSubscriptions(c.Email)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list"})
			return
		}
		for _, sub := range subs {
			out = append(out, subJSON{Endpoint: sub.Endpoint, Keys: keysJSON{P256dh: sub.P256dh, Auth: sub.Auth}})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
}

// sendPush delivers one encrypted web push. A 404/410 from the push service means
// the subscription is gone, so it is removed.
func (s *Server) sendPush(sub directory.PushSubscription, payload []byte) {
	pub, priv, err := vapidKeys(s.secret)
	if err != nil {
		return
	}
	resp, err := webpush.SendNotification(payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		Subscriber:      "mailto:postmaster@" + s.hostname,
		VAPIDPublicKey:  pub,
		VAPIDPrivateKey: priv,
		TTL:             60,
	})
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		if store, ok := s.auth.(pushStore); ok {
			_ = store.DeletePushSubscription(sub.Endpoint)
		}
	}
}

// StartPushPoller starts a background loop that watches push subscribers' inbox
// totals and sends a web push when new mail arrives; it is a no-op without a push
// store and stops with the context. A user's first poll only records a baseline, so
// the mail already present when they subscribe is not announced - only a later
// increase pushes. A total only rises on a new message (a read or flag leaves it
// unchanged, a delete lowers it), so this does not misfire on routine activity.
//
// This poller is deliberately NOT wired to the central push relay (unlike the
// MAPI/HTTP, EAS, and EWS long-polls). It is a single global loop that re-scans
// every subscriber per tick, so waking it on a change would re-scan all subscribers
// for each event - O(events x subscribers), strictly worse than its fixed-interval
// scan. Web push is not latency-critical the way a held protocol long-poll is. If
// low-latency web push is later wanted, the targeted approach is to invert the
// email->path resolution into a path->subscriber map and, on a path-matched wake,
// re-check only that one subscriber - not to wake this whole-fleet scan.
func (s *Server) StartPushPoller(ctx context.Context, interval time.Duration) {
	store, ok := s.auth.(pushStore)
	if !ok {
		return
	}
	go func() {
		seen := map[string]int{}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.safePoll(store, seen)
			}
		}
	}()
}

// safePoll runs one poll, recovering from any panic so a push-poll bug can never
// crash the mail-serving webmail process.
func (s *Server) safePoll(store pushStore, seen map[string]int) {
	defer func() { _ = recover() }()
	s.pollPushOnce(store, seen)
}

// pollPushOnce checks every subscriber's inbox once and pushes where it grew.
func (s *Server) pollPushOnce(store pushStore, seen map[string]int) {
	emails, err := store.PushSubscriberEmails()
	if err != nil {
		return
	}
	live := make(map[string]bool, len(emails))
	for _, email := range emails {
		live[email] = true
		total, ok := s.inboxTotal(email)
		if !ok {
			continue
		}
		prev, known := seen[email]
		seen[email] = total
		if known && total > prev {
			s.pushNewMail(store, email, total-prev)
		}
	}
	// Forget users who unsubscribed, so a later re-subscribe starts from a fresh
	// baseline rather than firing on the gap.
	for email := range seen {
		if !live[email] {
			delete(seen, email)
		}
	}
}

// inboxTotal returns a user's inbox message count.
func (s *Server) inboxTotal(email string) (int, bool) {
	path, ok := s.accounts.Resolve(email)
	if !ok {
		return 0, false
	}
	st, err := objectstore.Open(path)
	if err != nil {
		return 0, false
	}
	defer st.Close()
	total, _, err := st.CountMessages(mapi.PrivateFIDInbox)
	if err != nil {
		return 0, false
	}
	return total, true
}

// pushNewMail sends a new-mail web push to all of a user's subscriptions.
func (s *Server) pushNewMail(store pushStore, email string, n int) {
	subs, err := store.ListPushSubscriptions(email)
	if err != nil {
		return
	}
	body := "You have new mail"
	if n > 1 {
		body = fmt.Sprintf("You have %d new messages", n)
	}
	payload, err := json.Marshal(map[string]string{"title": "New email", "body": body, "url": "/inbox"})
	if err != nil {
		return
	}
	for _, sub := range subs {
		s.sendPush(sub, payload)
	}
}
