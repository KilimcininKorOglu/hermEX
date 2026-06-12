package webmail

import (
	"fmt"
	stdmime "mime"
	"net/http"
	"strings"
	"time"

	"hermex/internal/mta"
	"hermex/internal/store"
)

// sentName is the folder composed mail is filed into.
const sentName = "Sent"

func (s *Server) handleComposeForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessionFrom(r); !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, "compose", map[string]any{})
}

func (s *Server) handleComposeSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	to := strings.TrimSpace(r.FormValue("to"))
	subject := r.FormValue("subject")
	body := r.FormValue("body")

	recipients := splitAddresses(to)
	if len(recipients) == 0 {
		s.render(w, "compose", map[string]any{"Error": "At least one recipient is required.", "To": to, "Subject": subject, "Body": body})
		return
	}

	raw := buildMessage(sess.user, to, subject, body, s.hostname)
	unresolved, err := mta.Deliver(s.accounts, recipients, raw, time.Now())
	if err != nil {
		s.render(w, "compose", map[string]any{"Error": "Delivery failed: " + err.Error(), "To": to, "Subject": subject, "Body": body})
		return
	}
	if err := saveToSent(sess.mailboxPath, raw); err != nil {
		s.render(w, "compose", map[string]any{"Error": "Saved no Sent copy: " + err.Error(), "To": to, "Subject": subject, "Body": body})
		return
	}

	// Local recipients are delivered and a Sent copy is stored. If some
	// addresses have no local mailbox, report them (there is no relay yet)
	// rather than pretend they were delivered.
	if len(unresolved) > 0 {
		s.render(w, "compose", map[string]any{
			"Notice": "Delivered locally and saved to Sent. No local mailbox (and no external relay yet) for: " + strings.Join(unresolved, ", "),
		})
		return
	}
	http.Redirect(w, r, "/mail?folder="+sentName, http.StatusSeeOther)
}

// buildMessage assembles an RFC 5322 text/plain message from the compose form.
func buildMessage(from, to, subject, body, hostname string) []byte {
	// Normalize the textarea's line endings to CRLF for the wire/store.
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", stdmime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", randomToken()[:24], hostname)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	if !strings.HasSuffix(b.String(), "\r\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

// saveToSent appends a copy of a sent message to the sender's Sent folder,
// creating it on first use and marking the copy \Seen.
func saveToSent(mailboxPath string, raw []byte) error {
	st, err := store.Open(mailboxPath)
	if err != nil {
		return err
	}
	defer st.Close()
	sent, ok, err := st.FolderByName(nil, sentName)
	if err != nil {
		return err
	}
	if !ok {
		if sent, err = st.CreateFolder(nil, sentName); err != nil {
			return err
		}
	}
	_, err = st.AppendMessage(sent, raw, time.Now(), store.FlagSeen)
	return err
}

// splitAddresses splits a comma-separated recipient field into trimmed,
// non-empty addresses.
func splitAddresses(s string) []string {
	var out []string
	for addr := range strings.SplitSeq(s, ",") {
		if a := strings.TrimSpace(addr); a != "" {
			out = append(out, a)
		}
	}
	return out
}
