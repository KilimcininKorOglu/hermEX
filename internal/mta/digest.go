package mta

import (
	"bytes"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/url"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/quarantine"
)

// DigestDirectory is the directory surface the quarantine-digest worker needs: the
// mailboxes to summarize (each user resolved to its maildir) and the per-mailbox
// watermark that bounds a run to messages newer than the last digest.
type DigestDirectory interface {
	ListUsers() ([]directory.UserInfo, error)
	Resolve(address string) (string, bool)
	GetDigestWatermark(maildir string) (uint32, error)
	SetDigestWatermark(maildir string, uid uint32) error
}

// DigestRunner builds and delivers the periodic quarantine digest: a per-user
// plain-text summary of newly quarantined (Junk) mail, each entry carrying a signed,
// expiring link that releases that one message back to the inbox. The digest is
// appended directly to the user's own inbox (no SMTP loop, and it bypasses scoring so
// it is never itself junked). It runs only when a signing secret and base URL are
// configured.
type DigestRunner struct {
	Dir      DigestDirectory
	Secret   []byte        // shared signing key for release tokens; empty disables the run
	BaseURL  string        // externally-reachable webmail root, e.g. "https://mail.example.com"
	Hostname string        // for the digest's From address
	TokenTTL time.Duration // how long a release link stays valid (interval + grace)
	Now      func() time.Time
	Logger   *logging.Logger
}

// Run delivers a digest to every mailbox with quarantined mail newer than its
// watermark and returns how many digests were sent. A mailbox that cannot be opened or
// read is skipped, so one bad store never fails the run. It is a no-op without a
// configured secret or base URL.
func (r *DigestRunner) Run() int {
	if len(r.Secret) == 0 || strings.TrimSpace(r.BaseURL) == "" {
		return 0
	}
	users, err := r.Dir.ListUsers()
	if err != nil {
		r.logErr("", "digest.list", err)
		return 0
	}
	sent := 0
	for _, u := range users {
		maildir, ok := r.Dir.Resolve(u.Username)
		if !ok {
			continue
		}
		delivered, err := r.digestMailbox(u.Username, maildir)
		if err != nil {
			r.logErr(u.Username, "digest.deliver", err)
		}
		if delivered {
			sent++
		}
	}
	return sent
}

// digestMailbox summarizes one mailbox's quarantined mail newer than its watermark,
// delivers the digest to its inbox, and advances the watermark. It reports whether a
// digest was delivered. Nothing new means no digest (no empty or duplicate summaries).
func (r *DigestRunner) digestMailbox(email, maildir string) (bool, error) {
	last, err := r.Dir.GetDigestWatermark(maildir)
	if err != nil {
		return false, err
	}
	st, err := objectstore.Open(maildir)
	if err != nil {
		return false, nil // unprovisioned/unopenable mailbox → skip, never fail the run
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDJunk))
	if err != nil {
		return false, nil
	}
	var fresh []objectstore.MessageInfo
	maxUID := last
	for _, m := range msgs {
		if m.UID > last {
			fresh = append(fresh, m)
			if m.UID > maxUID {
				maxUID = m.UID
			}
		}
	}
	if len(fresh) == 0 {
		return false, nil
	}
	raw, err := r.buildDigest(email, fresh, r.Now())
	if err != nil {
		return false, err
	}
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, r.Now(), 0); err != nil {
		return false, err
	}
	// Advance the watermark only after delivery. If this fails, the worst case is a
	// duplicate digest next run — preferable to losing the notification entirely.
	if err := r.Dir.SetDigestWatermark(maildir, maxUID); err != nil {
		return true, err
	}
	return true, nil
}

// buildDigest assembles the plain-text RFC 5322 digest. Quarantined subjects and
// senders are attacker-authored, so the digest is deliberately plain text (no HTML to
// inject into) and each is flattened to a single line before inclusion.
func (r *DigestRunner) buildDigest(email string, msgs []objectstore.MessageInfo, when time.Time) ([]byte, error) {
	from := "postmaster@" + hostOrLocal(r.Hostname)
	expiry := when.Add(r.TokenTTL)

	var body strings.Builder
	fmt.Fprintf(&body, "%d message(s) were filed to your Junk folder as suspected spam since your last summary.\r\n", len(msgs))
	body.WriteString("To move one back to your inbox, open its release link and confirm on the page that opens.\r\n\r\n")
	for i, m := range msgs {
		token, err := quarantine.Mint(r.Secret, quarantine.Claims{Mailbox: email, UID: m.UID, Expiry: expiry.Unix()})
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&body, "  %d. Subject: %s\r\n", i+1, fieldOr(m.Subject, "(no subject)"))
		fmt.Fprintf(&body, "     From:    %s\r\n", fieldOr(m.Sender, "(unknown sender)"))
		fmt.Fprintf(&body, "     Date:    %s\r\n", m.InternalDate.UTC().Format(dateLayout))
		fmt.Fprintf(&body, "     Release: %s\r\n\r\n", releaseURL(r.BaseURL, token))
	}
	fmt.Fprintf(&body, "These release links expire on %s. You can also review your Junk folder in webmail.\r\n",
		expiry.UTC().Format(dateLayout))

	var b bytes.Buffer
	writeReplyField(&b, "From", from)
	writeReplyField(&b, "To", email)
	writeReplyField(&b, "Subject", mime.QEncoding.Encode("utf-8", fmt.Sprintf("Quarantine summary: %d new message(s)", len(msgs))))
	writeReplyField(&b, "Date", when.UTC().Format(dateLayout))
	writeReplyField(&b, "Message-ID", "<"+newToken()+"@"+hostOrLocal(r.Hostname)+">")
	// A system notification, not a reply: mark it auto-generated so no auto-responder
	// (including ours) replies to it (RFC 3834).
	writeReplyField(&b, "Auto-Submitted", "auto-generated")
	writeReplyField(&b, "MIME-Version", "1.0")
	writeReplyField(&b, "Content-Type", "text/plain; charset=utf-8")
	writeReplyField(&b, "Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(body.String()))
	qp.Close()
	return b.Bytes(), nil
}

// releaseURL builds the release link for a token against the configured webmail root.
func releaseURL(baseURL, token string) string {
	return strings.TrimRight(baseURL, "/") + "/quarantine/release?t=" + url.QueryEscape(token)
}

// fieldOr flattens attacker-controlled header text to a single line (reusing oneLine,
// which collapses CR/LF) for safe inclusion in the plain-text body, substituting
// fallback when it is empty.
func fieldOr(s, fallback string) string {
	if s = oneLine(s); s == "" {
		return fallback
	}
	return s
}

// hostOrLocal returns the hostname or "localhost" when it is empty, for synthesized
// addresses and Message-IDs.
func hostOrLocal(h string) string {
	if h = strings.TrimSpace(h); h != "" {
		return h
	}
	return "localhost"
}

// logErr records a digest worker failure when a logger is configured.
func (r *DigestRunner) logErr(user, event string, err error) {
	if r.Logger == nil {
		return
	}
	r.Logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: event, User: user, Fields: logging.Fields{"error": err.Error()}})
}
