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
	"hermex/internal/relay"
	"hermex/internal/smtp"
)

// Backend is an smtp.Backend that delivers to per-recipient mailbox stores.
type Backend struct {
	Accounts directory.Accounts
	Spool    *relay.Spool    // outbound relay queue; nil disables external relay
	Logger   *logging.Logger // central activity log; nil disables logging
}

// NewSession implements smtp.Backend.
func (b *Backend) NewSession(remoteAddr string) (smtp.Session, error) {
	return &session{accounts: b.Accounts, spool: b.Spool, logger: b.Logger, remoteAddr: remoteAddr}, nil
}

type session struct {
	accounts     directory.Accounts
	spool        *relay.Spool
	logger       *logging.Logger
	remoteAddr   string
	from         string
	targets      []target // local recipients, filed into mailboxes
	relayTargets []string // external recipients, spooled for outbound relay
	authUser     string   // set on a successful AUTH; empty for unauthenticated intake
	authMailbox  string   // the authenticated user's mailbox store path
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
	// The SMTP privilege gates authenticated submission only; inbound intake never
	// authenticates, so a user whose SMTP privilege is revoked can still receive
	// mail but cannot submit. Fail closed (discard ok) to match every other
	// protocol's gate: a privilege lookup that fails after a successful
	// Authenticate denies submission rather than waving it through.
	if privs, _ := authn.Privileges(username); !privs.SMTP {
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
	if s.authUser != "" {
		if err := overSendQuota(s.accounts, from); err != nil {
			return err
		}
	}
	s.from = from
	return nil
}

// authorizedSender reports whether the authenticated user may use from as the
// envelope sender. It is allowed when from is an address the user owns (their own
// primary or alias) or when the mailbox that owns from has granted the user a
// send-as permission. The send-as path fails closed: only a grant that can be
// positively confirmed lets an authenticated user put another mailbox in the From.
func (s *session) authorizedSender(from string) bool {
	want := strings.ToLower(strings.TrimSpace(from))
	ids := s.identities()
	if containsFold(ids, want) {
		return true
	}
	return s.grantedSendAs(want, ids)
}

// grantedSendAs reports whether one of the authenticated user's identities appears
// in the send-as list of the mailbox that owns from. It fails closed: an address
// that resolves to no local mailbox, a store that will not open, or an unreadable
// list denies the grant rather than risking a forged sender.
func (s *session) grantedSendAs(from string, ids []string) bool {
	path, ok := s.accounts.Resolve(from)
	if !ok {
		return false
	}
	st, err := objectstore.Open(path)
	if err != nil {
		return false
	}
	defer st.Close()
	list, err := st.GetSendAs()
	if err != nil {
		return false
	}
	for _, g := range list {
		if containsFold(ids, strings.ToLower(strings.TrimSpace(g))) {
			return true
		}
	}
	return false
}

// containsFold reports whether want (already lowercased) equals any address in
// list, compared case-insensitively after trimming.
func containsFold(list []string, want string) bool {
	for _, a := range list {
		if strings.ToLower(strings.TrimSpace(a)) == want {
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

// Rcpt routes one recipient. A recipient that resolves to a local mailbox is
// filed there. A recipient that does not resolve is refused unless this is an
// authenticated submission relaying to an external domain: only an authenticated
// user may relay (no open relay), and only to a domain this server is not
// authoritative for — an unresolved address in a local domain is a genuine
// user-unknown that must never be relayed (it would loop straight back).
func (s *session) Rcpt(to string) error {
	// A distribution-list recipient expands to its members. The posting-privilege
	// gate refuses here (a 550 — no message accepted, no backscatter, exactly like
	// the receive-quota gate); members are then routed leniently, since a stale
	// member must never fail delivery to the rest of the list.
	if exp, ok := s.accounts.(MListExpander); ok {
		leaves, isList, res, err := expandMailingList(exp, s.from, to)
		if err != nil {
			return fmt.Errorf("cannot route <%s>: %w", to, err)
		}
		if isList {
			if res != directory.MListOK {
				return fmt.Errorf("5.7.1 posting to list <%s> is not permitted", to)
			}
			for _, m := range leaves {
				s.routeListMember(m)
			}
			return nil
		}
	}
	return s.routeRecipient(to)
}

// routeRecipient files a single ordinary recipient: a local mailbox becomes a
// delivery target; an unresolved address is refused unless this is an
// authenticated submission relaying to an external domain.
func (s *session) routeRecipient(to string) error {
	if path, ok := s.accounts.Resolve(to); ok {
		if err := overReceiveQuota(path); err != nil {
			return err
		}
		s.targets = append(s.targets, target{addr: to, path: path})
		return nil
	}
	if s.authUser == "" {
		return fmt.Errorf("relay denied for <%s>", to)
	}
	external, err := isExternalDomain(s.accounts, to)
	if err != nil {
		return fmt.Errorf("cannot route <%s>: %w", to, err)
	}
	if !external {
		return fmt.Errorf("no such user <%s>", to)
	}
	if s.spool == nil {
		return fmt.Errorf("relay denied for <%s>", to)
	}
	s.relayTargets = append(s.relayTargets, to)
	return nil
}

// routeListMember files one expanded distribution-list member: a local mailbox
// becomes a delivery target (skipped when over its receive quota), an external
// member is relayed when a spool is available. A member that resolves to nothing
// is dropped — a list must not bounce because one member went stale.
func (s *session) routeListMember(m string) {
	if path, ok := s.accounts.Resolve(m); ok {
		if overReceiveQuota(path) == nil {
			s.targets = append(s.targets, target{addr: m, path: path})
		}
		return
	}
	if s.spool != nil {
		if ext, err := isExternalDomain(s.accounts, m); err == nil && ext {
			s.relayTargets = append(s.relayTargets, m)
		}
	}
}

// overReceiveQuota refuses a local recipient whose mailbox already sits at or
// above its receive quota, so an over-quota mailbox is rejected permanently at
// RCPT (no message accepted, no bounce backscatter). A store open or read error
// does NOT block delivery — quota is a soft administrative limit, never a reason
// to lose mail on an infrastructure hiccup. The limit is in KiB and 0 means
// unlimited; the comparison is done in 64-bit since limit*1024 overflows uint32.
func overReceiveQuota(path string) error {
	st, err := objectstore.Open(path)
	if err != nil {
		return nil
	}
	defer st.Close()
	q, err := st.GetQuota()
	if err != nil || q.ReceiveKB == 0 {
		return nil
	}
	size, err := st.MailboxSize()
	if err != nil {
		return nil
	}
	if size > int64(q.ReceiveKB)*1024 {
		return fmt.Errorf("mailbox is full (over receive quota)")
	}
	return nil
}

// overSendQuota refuses an outbound submission when the sender's own mailbox is
// at or above its send quota. The sender is resolved to a local mailbox; an
// address with no local mailbox (or a store error) is not blocked — send quota
// governs only local senders, and an infra hiccup must never strand a user's
// mail. The limit is in KiB, 0 means unlimited, and the comparison is 64-bit.
func overSendQuota(accounts directory.Accounts, sender string) error {
	path, ok := accounts.Resolve(sender)
	if !ok {
		return nil
	}
	st, err := objectstore.Open(path)
	if err != nil {
		return nil
	}
	defer st.Close()
	q, err := st.GetQuota()
	if err != nil || q.SendKB == 0 {
		return nil
	}
	size, err := st.MailboxSize()
	if err != nil {
		return nil
	}
	if size > int64(q.SendKB)*1024 {
		return fmt.Errorf("mailbox is full (over send quota)")
	}
	return nil
}

// isExternalDomain reports whether rcpt's domain lies outside this server's
// authority, so it may be relayed rather than delivered. It fails closed: when
// the directory cannot enumerate local domains, no domain can be confirmed
// external, so the recipient is treated as local (and thus undeliverable here
// rather than relayed), which avoids an accidental open relay or mail loop.
func isExternalDomain(accounts directory.Accounts, rcpt string) (bool, error) {
	ld, ok := accounts.(directory.LocalDomains)
	if !ok {
		return false, nil
	}
	i := strings.LastIndex(rcpt, "@")
	if i < 0 || i == len(rcpt)-1 {
		return false, nil // no domain part: cannot be confirmed external
	}
	local, err := ld.IsLocalDomain(rcpt[i+1:])
	if err != nil {
		return false, err
	}
	return !local, nil
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
	// External recipients are handed to the durable relay spool. Once Enqueue
	// commits, the worker owns their delivery (and retry), so returning success
	// here lets the server answer 250 — the message is no longer at risk of loss.
	if len(s.relayTargets) > 0 {
		if err := s.spool.Enqueue(s.from, s.relayTargets, raw, received); err != nil {
			s.logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "relay.fail", User: s.authUser, RemoteAddr: s.remoteAddr, Fields: logging.Fields{"from": s.from, "recipients": len(s.relayTargets)}, Err: err.Error()})
			return err
		}
		s.logger.Emit(logging.Event{Level: logging.LevelInfo, Subsystem: logging.MTA, Name: "relay.queued", User: s.authUser, RemoteAddr: s.remoteAddr, Fields: logging.Fields{"from": s.from, "recipients": len(s.relayTargets)}})
	}
	return nil
}

func (s *session) Reset()        { s.from = ""; s.targets = nil; s.relayTargets = nil }
func (s *session) Logout() error { return nil }

// Deliver resolves each recipient address to its local mailbox and appends the
// raw message to that mailbox's INBOX. from is the envelope sender (the
// return-path), used as the destination of any out-of-office auto-reply.
// Addresses with no local mailbox are returned as unresolved, so callers can
// report partial delivery rather than silently dropping them. Deliver never
// relays: automated notifications (auto-reply, read receipt, bounce) use it so a
// message can never be sent off-server. User-composed send paths that should
// relay external recipients use DeliverAndRelay.
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

// DeliverAndRelay is Deliver plus outbound relay: after local delivery, each
// recipient with no local mailbox that belongs to a foreign domain is queued in
// spool for delivery to its mail exchanger, instead of being returned
// unresolved. from is the relay envelope sender. Only authorized, user-composed
// send paths pass a non-nil spool; with a nil spool it is exactly Deliver.
//
// The returned unresolved holds only the genuinely undeliverable — a user-unknown
// in a local domain, or (when spool is nil) every external address.
func DeliverAndRelay(accounts directory.Accounts, spool *relay.Spool, from string, recipients []string, raw []byte, received time.Time) (unresolved []string, err error) {
	if err := overSendQuota(accounts, from); err != nil {
		return recipients, err
	}
	// A distribution-list recipient expands to its members before delivery; a list
	// whose posting privilege refuses this sender is reported as undeliverable.
	leaves, refused := expandRecipientList(accounts, from, recipients)
	// A recipient with a mail-forward directive routes a copy to its destination; a
	// Redirect also drops the local copy. Destinations join the delivery set and flow
	// through the same local-then-relay path below, so a destination in a foreign
	// domain is relayed and an undeliverable one surfaces as unresolved for the caller
	// to bounce — never a silent drop.
	leaves, dests := applyForwards(accounts, leaves)
	leaves = append(leaves, dests...)
	unresolved, err = Deliver(accounts, from, leaves, raw, received)
	if err != nil {
		return append(unresolved, refused...), err
	}
	if spool != nil && len(unresolved) > 0 {
		var external, stuck []string
		for _, rcpt := range unresolved {
			if ext, e := isExternalDomain(accounts, rcpt); e == nil && ext {
				external = append(external, rcpt)
			} else {
				stuck = append(stuck, rcpt)
			}
		}
		if len(external) > 0 {
			if e := spool.Enqueue(from, external, raw, received); e != nil {
				return append(stuck, refused...), e
			}
		}
		unresolved = stuck
	}
	return append(unresolved, refused...), nil
}

// applyForwards consults each resolved recipient's mail-forward directive and splits
// the set into the addresses delivered to their own mailbox (every recipient without
// a forward, and every CC recipient — which keeps its local copy) and the forward
// destinations to route. A Redirect recipient is dropped from the local set so only
// the destination receives it. Destinations are de-duplicated and a self-forward
// (destination equal to the recipient) is ignored. A directory without a Forwarder
// has no forwarding and the recipients pass through unchanged.
//
// The destinations are routed by the caller through the ordinary local-then-relay
// path, so a forwarded copy is never itself re-forwarded (one hop) and an
// undeliverable destination becomes unresolved rather than vanishing. The copy keeps
// the original envelope sender: a copy relayed to a foreign domain may therefore fail
// SPF/DMARC and bounce to the original sender — sender rewriting (SRS) is a later
// refinement, deliberately omitted in v1.
func applyForwards(accounts directory.Accounts, recipients []string) (locals, dests []string) {
	fwder, ok := accounts.(directory.Forwarder)
	if !ok {
		return recipients, nil
	}
	seen := map[string]bool{}
	for _, rcpt := range recipients {
		fi, has, err := fwder.GetForward(rcpt)
		if err != nil || !has || fi.Destination == "" || strings.EqualFold(fi.Destination, rcpt) {
			locals = append(locals, rcpt)
			continue
		}
		if fi.Type == directory.ForwardCC {
			locals = append(locals, rcpt)
		}
		dest := strings.ToLower(strings.TrimSpace(fi.Destination))
		if !seen[dest] {
			seen[dest] = true
			dests = append(dests, dest)
		}
	}
	return locals, dests
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
