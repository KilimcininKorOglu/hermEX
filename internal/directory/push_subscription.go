package directory

import (
	"errors"
	"strings"
)

// PushSubscription is one browser web-push subscription - a PushManager endpoint
// plus its encryption keys - so the webmail poll loop can deliver a new-mail push to
// the user's devices. Endpoint is the natural key (unique per browser subscription).
type PushSubscription struct {
	Endpoint  string
	Email     string
	P256dh    string
	Auth      string
	CreatedAt int64
}

// SavePushSubscription stores or refreshes a subscription, keyed by endpoint, so a
// browser re-subscribing with new keys updates its row rather than duplicating it.
func (d *SQLDirectory) SavePushSubscription(s PushSubscription) error {
	if s.Endpoint == "" || s.Email == "" || s.P256dh == "" || s.Auth == "" {
		return errors.New("push subscription needs endpoint, email, p256dh and auth")
	}
	_, err := d.db.Exec(
		`INSERT INTO push_subscriptions (endpoint, email, p256dh, auth, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE email = VALUES(email), p256dh = VALUES(p256dh), auth = VALUES(auth)`,
		s.Endpoint, strings.ToLower(s.Email), s.P256dh, s.Auth, s.CreatedAt)
	return err
}

// ListPushSubscriptions returns a user's subscriptions (their browser devices).
func (d *SQLDirectory) ListPushSubscriptions(email string) ([]PushSubscription, error) {
	rows, err := d.db.Query(
		`SELECT endpoint, email, p256dh, auth, created_at
		   FROM push_subscriptions WHERE email = ? ORDER BY created_at`,
		strings.ToLower(email))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		var s PushSubscription
		if err := rows.Scan(&s.Endpoint, &s.Email, &s.P256dh, &s.Auth, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeletePushSubscription removes a subscription by endpoint - on an explicit
// unsubscribe, or after the push service reports the endpoint gone (HTTP 404/410).
func (d *SQLDirectory) DeletePushSubscription(endpoint string) error {
	_, err := d.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

// PushSubscriberEmails returns the distinct emails that have at least one push
// subscription, so the poll loop only watches mailboxes a device is listening to.
func (d *SQLDirectory) PushSubscriberEmails() ([]string, error) {
	rows, err := d.db.Query(`SELECT DISTINCT email FROM push_subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
