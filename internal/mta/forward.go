package mta

import (
	"context"
	"net"
	"time"

	"hermex/internal/arc"
	"hermex/internal/dkimsign"
	"hermex/internal/logging"
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
	return srs.Forward(from, domain, f.SRSSecret, now)
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

// skip records a forward that left without an ARC set.
func (f *Forwarding) skip(domain, detail string, err error) {
	ev := logging.Event{Level: logging.LevelWarn, Subsystem: logging.MTA, Name: "arc.skip",
		Fields: logging.Fields{"domain": domain, "detail": detail}}
	if err != nil {
		ev.Err = err.Error()
	}
	f.Logger.Emit(ev)
}
