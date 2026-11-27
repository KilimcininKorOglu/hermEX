package mta

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"hermex/internal/arc"
	"hermex/internal/directory"
	"hermex/internal/dkimsign"
	"hermex/internal/logging"
	"hermex/internal/relay"
	"hermex/internal/smtp"
	"hermex/internal/srs"
)

// arcLookupBudget bounds every DNS query one seal makes. The chain a message
// carries names its sealers' domains, so the sender chooses which nameservers
// the seal waits on.
const arcLookupBudget = 10 * time.Second

// Forwarding vouches for mail this server relays but did not author: a list
// post or a forward. The author's DKIM signature is never added to such mail;
// instead the forwarding domain seals it with ARC and rewrites its envelope
// sender with SRS, so the next hop can tell who relayed it and SPF checks a
// domain this server sends for.
type Forwarding struct {
	Keys       dkimsign.KeyProvider // the forwarding domain's signing key seals the ARC set
	SRSSecret  []byte               // keys the SRS hash; empty leaves envelope senders as they are
	AuthServID string               // authserv-id of the Authentication-Results the seal records
	Lookup     arc.LookupTXT        // nil uses the system resolver under arcLookupBudget
	Logger     *logging.Logger
}

// ReversePath returns the envelope sender a forward by domain carries: from
// rewritten with SRS. The null sender stays null, since a bounce is never
// rewritten.
func (f *Forwarding) ReversePath(from, domain string, now time.Time) string {
	if f == nil || len(f.SRSSecret) == 0 || from == "" || domain == "" {
		return from
	}
	out, err := srs.Forward(from, domain, f.SRSSecret, now)
	if err != nil {
		// The forward still goes out, as it does with SRS off; the failure is
		// recorded so the operator sees why its envelope sender was kept.
		f.Logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "srs.rewrite.fail",
			User: from, Fields: logging.Fields{"domain": domain}, Err: err.Error()})
		return from
	}
	return out
}

// Seal returns raw with an ARC set by domain on top. Mail is never held for a
// seal: without an enabled RSA key, or when the chain cannot take another set,
// raw goes out unsealed and the reason is recorded.
func (f *Forwarding) Seal(raw []byte, domain string, now time.Time) []byte {
	if f == nil || f.Keys == nil || domain == "" {
		return raw
	}
	key, selector, found, err := dkimsign.SigningKey(f.Keys, domain)
	if err != nil || !found {
		f.skip(domain, "no enabled signing key", err)
		return raw
	}
	lookup, cancel := f.lookup()
	defer cancel()
	sealer := arc.Sealer{Domain: domain, Selector: selector, Key: key, AuthServID: f.AuthServID, Lookup: lookup}
	out, err := sealer.Seal(raw, now)
	if err != nil {
		f.skip(domain, "seal refused", err)
		return raw
	}
	return out
}

// lookup returns the resolver one seal uses and the function that ends it.
func (f *Forwarding) lookup() (arc.LookupTXT, func()) {
	if f.Lookup != nil {
		return f.Lookup, func() {}
	}
	ctx, cancel := context.WithTimeout(context.Background(), arcLookupBudget)
	return func(name string) ([]string, error) { return net.DefaultResolver.LookupTXT(ctx, name) }, cancel
}

// srsBounce routes a recipient that is an SRS address at a hosted domain: the
// return path of mail this server forwarded. handled is false for any other
// address. Only a bounce (the null sender) may use one, and only an address
// this server minted within srs.MaxAge reaches the original sender; anything
// else is refused, so the rewritten addresses cannot be used as an open relay.
func (s *session) srsBounce(to string) (handled bool, err error) {
	if s.forwarding == nil || len(s.forwarding.SRSSecret) == 0 || !srs.IsSRS(to) {
		return false, nil
	}
	external, lerr := isExternalDomain(s.accounts, to)
	if lerr != nil {
		return true, s.tempRouteFailure(to, lerr)
	}
	if external {
		return false, nil
	}
	if s.from != "" {
		return true, &smtp.PermError{Message: fmt.Sprintf("5.7.1 <%s> accepts only delivery status notifications", to)}
	}
	orig, rerr := srs.Reverse(to, s.forwarding.SRSSecret, time.Now())
	if rerr != nil {
		return true, &smtp.PermError{Message: fmt.Sprintf("5.1.1 <%s> is not a valid return address", to)}
	}
	if path, ok := s.accounts.Resolve(orig); ok {
		return true, s.routeLocal(orig, path)
	}
	if s.spool == nil {
		return true, &smtp.PermError{Message: fmt.Sprintf("relay denied for <%s>", to)}
	}
	s.relayTargets = append(s.relayTargets, relay.DSNRecipient{Addr: orig})
	return true, nil
}

// routeResolved routes a recipient that resolved to a local mailbox, applying
// its forward setting: a CC forward keeps the local copy and adds the
// destination, a redirect sends only to the destination. A destination is
// routed once and never forwarded again, so forwards cannot loop. A forward
// setting that cannot be read, or a destination that cannot be routed, leaves
// the message in the local mailbox rather than losing it.
func (s *session) routeResolved(to, path, notify string) error {
	dest, keepLocal := s.forwardOf(to)
	if dest == "" {
		return s.routeLocal(to, path)
	}
	if keepLocal {
		if err := s.routeLocal(to, path); err != nil {
			return err
		}
	}
	routed, err := s.routeForwardDest(to, dest, notify)
	if err != nil || routed || keepLocal {
		return err
	}
	return s.routeLocal(to, path)
}

// forwardOf returns a recipient's forward destination and whether the local
// copy stays. No destination means plain local delivery.
func (s *session) forwardOf(to string) (dest string, keepLocal bool) {
	fwder, ok := s.accounts.(directory.Forwarder)
	if !ok {
		return "", true
	}
	fi, has, err := fwder.GetForward(to)
	if err != nil {
		s.logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "forward.lookup.fail", User: to, RemoteAddr: s.remoteAddr, Err: err.Error()})
		return "", true
	}
	dest = strings.ToLower(strings.TrimSpace(fi.Destination))
	if !has || dest == "" || strings.EqualFold(dest, to) {
		return "", true
	}
	return dest, fi.Type == directory.ForwardCC
}

// routeForwardDest routes one forward destination of owner. routed is false
// when the destination cannot be reached from here, so the caller keeps the
// message locally.
func (s *session) routeForwardDest(owner, dest, notify string) (routed bool, err error) {
	if path, ok := s.accounts.Resolve(dest); ok {
		if overReceiveQuota(path) != nil {
			return false, nil
		}
		s.targets = append(s.targets, target{addr: dest, path: path})
		return true, nil
	}
	relayable, err := isRelayable(s.accounts, dest)
	if err != nil {
		return false, s.tempRouteFailure(dest, err)
	}
	if !relayable || s.spool == nil {
		s.logger.Emit(logging.Event{Level: logging.LevelWarn, Subsystem: logging.MTA, Name: "forward.unroutable", User: owner, RemoteAddr: s.remoteAddr, Fields: logging.Fields{"destination": dest}})
		return false, nil
	}
	if s.outbound != nil && !s.outbound.Allow(owner) {
		return false, &smtp.TempError{Message: "4.7.4 too many recipients in a short time, please retry later"}
	}
	rcpt := relay.DSNRecipient{Addr: dest, Notify: notify}
	if s.authUser != "" {
		// An authenticated sender's From passed sendas, so the copy is author-signed.
		s.relayTargets = append(s.relayTargets, rcpt)
		return true, nil
	}
	s.addForward(domainPart(owner), rcpt)
	return true, nil
}

// skip records a forward that left without an ARC set.
func (f *Forwarding) skip(domain, detail string, err error) {
	ev := logging.Event{Level: logging.LevelWarn, Subsystem: logging.MTA, Name: "arc.skip",
		Fields: logging.Fields{"domain": domain, "detail": detail}}
	if err != nil {
		ev.Err = err.Error()
	}
	f.Logger.Emit(ev)
}
