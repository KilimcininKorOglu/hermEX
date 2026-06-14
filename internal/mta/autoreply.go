package mta

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"log"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// dateLayout is the RFC 5322 date form synthesized for the auto-reply.
const dateLayout = "Mon, 02 Jan 2006 15:04:05 -0700"

// maybeAutoReply sends a single out-of-office auto-reply to the sender of a
// just-delivered message, when the recipient mailbox has out-of-office active
// and the message is genuine person-to-person mail (see autoReplySuppressed).
//
// It is best-effort, exactly like the inbox-rule pass: any error or panic is
// logged and swallowed so it can never fail the delivery that triggered it (a
// failed delivery makes the sender retry and double-deliver). The reply carries
// "Auto-Submitted: auto-replied", which makes the recipient of the reply
// suppress their own auto-reply — that is what prevents an infinite loop
// between two mailboxes that both have out-of-office enabled.
//
// The loop break reads that header off the raw bytes handed to deliver, not the
// stored copy: oxcmail.Export does not re-emit Auto-Submitted, so any future
// feature that re-delivers a stored message (a forward/redirect rule action,
// say) must re-add the header itself, or the loop protection weakens.
func maybeAutoReply(accounts directory.Accounts, st *objectstore.Store, selfAddr, envelopeSender string, raw []byte, received time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("mta: out-of-office pass panicked for <%s>, skipped: %v", selfAddr, r)
		}
	}()
	if err := sendAutoReply(accounts, st, selfAddr, envelopeSender, raw, received); err != nil {
		log.Printf("mta: out-of-office pass failed for <%s>, skipped: %v", selfAddr, err)
	}
}

// sendAutoReply implements the out-of-office decision and send. It returns an
// error only for storage faults the caller logs; a decision not to reply
// (out-of-office off or outside its window, a suppressed sender, an external
// sender with external replies disabled, an unparseable incoming message) is a
// nil return, not an error.
func sendAutoReply(accounts directory.Accounts, st *objectstore.Store, selfAddr, envelopeSender string, raw []byte, received time.Time) error {
	cfg, err := st.GetOOFSettings()
	if err != nil {
		return err
	}
	if !cfg.OOFActive(received.Unix()) {
		return nil
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil // an unparseable incoming message is not replied to
	}
	// Reply to the return address (the envelope sender), not the From header
	// (RFC 3834 §4): the From may be spoofed or a list, the return path is who
	// the mail actually came from.
	to := bareAddress(envelopeSender)
	if to == "" {
		return nil
	}
	// Internal vs external is decided by whether the sender has a local mailbox,
	// not by string-matching domains.
	_, internal := accounts.Resolve(to)

	body, send := autoReplyDecision(msg.Header, envelopeSender, selfAddr, cfg, internal)
	if !send {
		return nil
	}

	reply := buildAutoReply(selfAddr, to, cfg.Subject, body, msg.Header.Get("Message-ID"), received)

	// Deliver the reply through the normal path. A local recipient receives it
	// in their inbox; its Auto-Submitted header makes their own out-of-office
	// suppress, so the exchange ends after one message. An external recipient
	// has no local mailbox and no relay exists yet, so the reply is reported
	// unresolved and dropped — consistent with the rest of the server.
	if _, err := Deliver(accounts, selfAddr, []string{to}, reply, received); err != nil {
		return err
	}
	return nil
}

// autoReplyDecision is the pure out-of-office decision for a mailbox whose
// out-of-office is already known to be active. It returns the reply body and
// whether to send. send is false for a suppressed sender (see
// autoReplySuppressed) and for an external sender (no local mailbox) when
// external replies are not enabled; internal senders always get the internal
// reply. Keeping the decision pure makes every branch — including the external
// one, which the delivery path cannot exercise without an outbound relay —
// unit-testable.
func autoReplyDecision(hdr mail.Header, envelopeSender, selfAddr string, cfg objectstore.OOFSettings, internal bool) (body string, send bool) {
	if autoReplySuppressed(hdr, envelopeSender, selfAddr) {
		return "", false
	}
	if internal {
		return cfg.InternalReply, true
	}
	if !cfg.ExternalEnabled {
		return "", false
	}
	return cfg.ExternalReply, true
}

// autoReplySuppressed reports whether an out-of-office auto-reply MUST NOT be
// sent in response to an incoming message, following RFC 3834 and common
// mailing-list conventions. It returns true (suppress) when any of these hold,
// so an ordinary person-to-person message — which carries none of them — is
// replied to:
//   - the envelope sender is empty or the null return-path "<>" (a bounce);
//   - the sender is this mailbox itself, or a role/no-reply mailbox
//     (postmaster, mailer-daemon, no-reply, ...);
//   - Auto-Submitted is present with a keyword other than "no" (automated mail,
//     including our own auto-replies — this is what breaks the reply loop);
//   - Precedence is bulk, list, or junk;
//   - the message is mailing-list traffic (List-Id / List-Unsubscribe / List-Post).
//
// The Auto-Submitted and Precedence tests suppress only when the header is
// present: an absent header marks an ordinary message that must be replied to
// (RFC 3834 §5). Treating absence as automated would silently suppress every
// reply.
func autoReplySuppressed(hdr mail.Header, envelopeSender, selfAddr string) bool {
	sender := bareAddress(envelopeSender)
	if sender == "" {
		return true // null return-path / bounce: never auto-reply
	}
	if strings.EqualFold(sender, bareAddress(selfAddr)) {
		return true // do not reply to ourselves
	}
	if isRoleMailbox(sender) {
		return true
	}
	if v := strings.TrimSpace(hdr.Get("Auto-Submitted")); v != "" && !strings.EqualFold(autoSubmittedKeyword(v), "no") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(hdr.Get("Precedence"))) {
	case "bulk", "list", "junk":
		return true
	}
	if hdr.Get("List-Id") != "" || hdr.Get("List-Unsubscribe") != "" || hdr.Get("List-Post") != "" {
		return true
	}
	return false
}

// autoSubmittedKeyword returns the leading keyword of an Auto-Submitted field
// value, dropping any ";"-separated parameters and "(comment)" so
// "auto-generated (rejected)" and "no; ..." each compare by keyword alone
// (RFC 3834 §5).
func autoSubmittedKeyword(v string) string {
	if i := strings.IndexAny(v, " ;("); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// bareAddress lowercases and reduces an address to its addr-spec, dropping any
// display name and angle brackets (best effort). The empty string and the null
// return-path "<>" both reduce to "".
func bareAddress(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "<>" {
		return ""
	}
	if a, err := mail.ParseAddress(s); err == nil {
		return strings.ToLower(a.Address)
	}
	return strings.ToLower(strings.Trim(s, "<>"))
}

// isRoleMailbox reports whether an address' local part is a role or no-reply
// mailbox that must never receive an auto-reply: such mailboxes are unattended,
// and replying to them risks a loop with another automaton.
func isRoleMailbox(addr string) bool {
	local := addr
	if i := strings.IndexByte(local, '@'); i >= 0 {
		local = local[:i]
	}
	switch strings.ToLower(local) {
	case "postmaster", "mailer-daemon", "no-reply", "noreply", "do-not-reply", "donotreply":
		return true
	}
	return false
}

// buildAutoReply assembles a minimal RFC 5322 plain-text auto-reply. It is
// hand-built rather than routed through oxcmail.Export because Export emits a
// fixed header set with no way to carry Auto-Submitted — the one header that
// breaks the reply loop.
func buildAutoReply(from, to, subject, body, inReplyTo string, when time.Time) []byte {
	if strings.TrimSpace(subject) == "" {
		subject = "Automatic reply"
	}
	var b bytes.Buffer
	writeReplyField(&b, "From", from)
	writeReplyField(&b, "To", to)
	writeReplyField(&b, "Subject", mime.QEncoding.Encode("utf-8", subject))
	writeReplyField(&b, "Date", when.UTC().Format(dateLayout))
	writeReplyField(&b, "Message-ID", "<"+newToken()+"@"+domainOf(from)+">")
	if inReplyTo != "" {
		writeReplyField(&b, "In-Reply-To", inReplyTo)
		writeReplyField(&b, "References", inReplyTo)
	}
	// The loop-break header. RFC 3834 §5: an auto-reply is marked auto-replied so
	// downstream responders (including ours) do not reply to it.
	writeReplyField(&b, "Auto-Submitted", "auto-replied")
	writeReplyField(&b, "MIME-Version", "1.0")
	writeReplyField(&b, "Content-Type", "text/plain; charset=utf-8")
	writeReplyField(&b, "Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(body))
	qp.Close()
	return b.Bytes()
}

// writeReplyField writes one "Name: value" header line terminated with CRLF.
func writeReplyField(b *bytes.Buffer, name, value string) {
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\r\n")
}

// domainOf returns the domain part of an address for the synthesized
// Message-ID, falling back to "localhost" when the address has no domain.
func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return "localhost"
}

// newToken returns a random hex token for the auto-reply Message-ID.
func newToken() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(buf[:])
}
