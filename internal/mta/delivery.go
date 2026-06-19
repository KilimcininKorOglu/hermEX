// Package mta delivers accepted SMTP messages into recipient mailboxes. It
// adapts the protocol-only smtp.Backend to the store, resolving recipients
// through a directory.Accounts and appending each message to the recipient's
// INBOX.
package mta

import (
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/smtp"
)

// Backend is an smtp.Backend that delivers to per-recipient mailbox stores.
type Backend struct {
	Accounts directory.Accounts
	Logger   *logging.Logger // central activity log; nil disables logging
}

// NewSession implements smtp.Backend.
func (b *Backend) NewSession(remoteAddr string) (smtp.Session, error) {
	return &session{accounts: b.Accounts, logger: b.Logger, remoteAddr: remoteAddr}, nil
}

type session struct {
	accounts    directory.Accounts
	logger      *logging.Logger
	remoteAddr  string
	from        string
	targets     []target
	authUser    string // set on a successful AUTH; empty for unauthenticated intake
	authMailbox string // the authenticated user's mailbox store path
}

// Auth implements smtp.Authenticator: it validates submission credentials against
// the directory and records the authenticated identity for send authorization. It
// reports false when the directory cannot authenticate or the credentials fail.
func (s *session) Auth(username, password string) bool {
	authn, ok := s.accounts.(directory.Authenticator)
	if !ok {
		return false
	}
	path, ok := authn.Authenticate(username, password)
	if !ok {
		return false
	}
	s.authUser, s.authMailbox = username, path
	return true
}

// target is one resolved recipient: the address it was accepted for (used as
// the From of an out-of-office auto-reply) and the mailbox store path it
// delivers to.
type target struct {
	addr string
	path string
}

// Mail records the envelope sender. On an authenticated submission it first
// enforces send-as authorization: the sender must be an address the logged-in
// user owns, so an authenticated account cannot forge mail from another. Inbound
// intake (no AUTH) keeps an unrestricted sender — a remote MTA legitimately
// relays mail from any origin.
func (s *session) Mail(from string) error {
	if s.authUser != "" && !s.authorizedSender(from) {
		return fmt.Errorf("5.7.1 <%s> is not an address you may send as", from)
	}
	s.from = from
	return nil
}

// authorizedSender reports whether the authenticated user may use from as the
// envelope sender, matching case-insensitively against the addresses the
// directory says they own.
func (s *session) authorizedSender(from string) bool {
	want := strings.ToLower(strings.TrimSpace(from))
	for _, a := range s.identities() {
		if strings.ToLower(a) == want {
			return true
		}
	}
	return false
}

// identities returns the addresses the authenticated user may send as. It fails
// closed exactly like the webmail compose gate: when the directory cannot
// enumerate identities, the user may still send as themselves but as no one
// else.
func (s *session) identities() []string {
	id, ok := s.accounts.(directory.Identifier)
	if !ok {
		return []string{s.authUser}
	}
	addrs, err := id.Identities(s.authUser)
	if err != nil || len(addrs) == 0 {
		return []string{s.authUser}
	}
	return addrs
}

func (s *session) Rcpt(to string) error {
	path, ok := s.accounts.Resolve(to)
	if !ok {
		return fmt.Errorf("relay denied for <%s>", to)
	}
	s.targets = append(s.targets, target{addr: to, path: path})
	return nil
}

func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	received := time.Now()
	for _, t := range s.targets {
		if err := deliver(s.accounts, s.from, t.addr, t.path, raw, received); err != nil {
			s.logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "delivery.fail", User: t.addr, RemoteAddr: s.remoteAddr, Fields: logging.Fields{"from": s.from}, Err: err.Error()})
			return err
		}
		s.logger.Emit(logging.Event{Level: logging.LevelInfo, Subsystem: logging.MTA, Name: "delivery.ok", User: t.addr, RemoteAddr: s.remoteAddr, Fields: logging.Fields{"from": s.from}})
	}
	return nil
}

func (s *session) Reset()        { s.from = ""; s.targets = nil }
func (s *session) Logout() error { return nil }

// Deliver resolves each recipient address to its local mailbox and appends the
// raw message to that mailbox's INBOX. from is the envelope sender (the
// return-path), used as the destination of any out-of-office auto-reply.
// Addresses with no local mailbox are returned as unresolved (there is no
// outbound relay yet), so callers can report partial delivery rather than
// silently dropping them.
func Deliver(accounts directory.Accounts, from string, recipients []string, raw []byte, received time.Time) (unresolved []string, err error) {
	for _, rcpt := range recipients {
		path, ok := accounts.Resolve(rcpt)
		if !ok {
			unresolved = append(unresolved, rcpt)
			continue
		}
		if err := deliver(accounts, from, rcpt, path, raw, received); err != nil {
			return unresolved, err
		}
	}
	return unresolved, nil
}

// deliver appends a raw message to the inbox of the mailbox at path. The inbox
// is a built-in folder provisioned when the mailbox is created, so it is
// addressed directly by its fixed id. from is the envelope sender and rcptAddr
// the address this mailbox was reached at; both feed the out-of-office pass.
func deliver(accounts directory.Accounts, from, rcptAddr, path string, raw []byte, received time.Time) error {
	st, err := objectstore.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()

	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, received, 0)
	if err != nil {
		return err
	}
	// The message is delivered the moment it is filed. Inbox rules and the
	// out-of-office auto-reply then run as best-effort decoration on top of that
	// successful delivery: any error or panic is logged and swallowed, never
	// returned, so a misbehaving rule or a failed auto-reply cannot fail delivery
	// and make the sender retry (which would duplicate the message).
	applyInboxRules(st, info)
	maybeAutoReply(accounts, st, rcptAddr, from, raw, received)
	return nil
}

// applyInboxRules runs the mailbox's inbox rules against a just-delivered
// message, swallowing any error or panic. See deliver for why a rule must never
// surface an error onto the delivery path.
func applyInboxRules(st *objectstore.Store, m objectstore.MessageInfo) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("mta: inbox rule pass panicked for uid %d, skipped: %v", m.UID, r)
		}
	}()
	if err := st.ApplyInboxRules(m); err != nil {
		log.Printf("mta: inbox rule pass failed for uid %d, skipped: %v", m.UID, err)
	}
}
