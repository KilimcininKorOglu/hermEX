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

// ErrPushEndpointTaken reports a save naming an endpoint another account already
// holds.
var ErrPushEndpointTaken = errors.New("directory: the push endpoint belongs to another account")

// SavePushSubscription stores or refreshes a subscription, keyed by endpoint, so a
// browser re-subscribing with new keys updates its row rather than duplicating it.
// An endpoint stays with the account that saved it first: a save by another account
// changes nothing and reports ErrPushEndpointTaken, because moving the row would send
// one user's mail notices to the other's browser.
func (d *SQLDirectory) SavePushSubscription(s PushSubscription) error {
	if s.Endpoint == "" || s.Email == "" || s.P256dh == "" || s.Auth == "" {
		return errors.New("push subscription needs endpoint, email, p256dh and auth")
	}
	email := strings.ToLower(s.Email)
	res, err := d.db.Exec(
		`INSERT INTO push_subscriptions (endpoint, email, p256dh, auth, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE p256dh = IF(email = VALUES(email), VALUES(p256dh), p256dh),
		   auth = IF(email = VALUES(email), VALUES(auth), auth)`,
		s.Endpoint, email, s.P256dh, s.Auth, s.CreatedAt)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	// Nothing changed: either the owner re-saved the same keys, or another account
	// named this endpoint.
	var owner string
	if err := d.db.QueryRow(`SELECT email FROM push_subscriptions WHERE endpoint = ?`, s.Endpoint).Scan(&owner); err != nil {
		return err
	}
	if owner != email {
		return ErrPushEndpointTaken
	}
	return nil
}

// ListPushSubscriptions returns a user's subscriptions (their browser devices).
func (d *SQLDirectory) ListPushSubscriptions(email string) (out []PushSubscription, err error) {
	rows, err := d.db.Query(
		`SELECT endpoint, email, p256dh, auth, created_at
		   FROM push_subscriptions WHERE email = ? ORDER BY created_at`,
		strings.ToLower(email))
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
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
func (d *SQLDirectory) PushSubscriberEmails() (out []string, err error) {
	rows, err := d.db.Query(`SELECT DISTINCT email FROM push_subscriptions`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
