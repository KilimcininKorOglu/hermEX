package webmail

import (
	"fmt"
	stdmime "mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mta"
	"hermex/internal/store"
)

// sentName is the folder composed mail is filed into.
const sentName = "Sent"

// composeView is the data the compose template renders, covering both a blank
// compose and a reply/forward prefill.
type composeView struct {
	Title        string
	To           string
	Cc           string
	Subject      string
	Body         string
	InReplyTo    string // carried as a hidden field, written as In-Reply-To on send
	References   string // carried as a hidden field, written as References on send
	AttachFolder string // forward-as-attachment: source folder to embed at send
	AttachUID    string // forward-as-attachment: source uid to embed at send
	Error        string
	Notice       string
}

func (s *Server) handleComposeForm(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	action := r.URL.Query().Get("action")
	if action == "" {
		s.render(w, "compose", composeView{Title: "New message"})
		return
	}
	// Reply/forward variants prefill from a source message.
	folder := r.URL.Query().Get("folder")
	uid64, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	if err != nil {
		s.render(w, "compose", composeView{Title: "New message"})
		return
	}
	st, err := store.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		http.Error(w, "cannot read folders", http.StatusInternalServerError)
		return
	}
	folderID, found := resolveFolder(folders, folder)
	if !found {
		http.NotFound(w, r)
		return
	}
	raw, err := st.GetMessageRaw(folderID, uint32(uid64))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "compose", buildComposeFromSource(action, folder, uint32(uid64), raw, sess.user))
}

func (s *Server) handleComposeSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	v := composeView{
		Title:        "New message",
		To:           strings.TrimSpace(r.FormValue("to")),
		Cc:           strings.TrimSpace(r.FormValue("cc")),
		Subject:      r.FormValue("subject"),
		Body:         r.FormValue("body"),
		InReplyTo:    strings.TrimSpace(r.FormValue("inreplyto")),
		References:   strings.TrimSpace(r.FormValue("references")),
		AttachFolder: r.FormValue("attachfolder"),
		AttachUID:    r.FormValue("attachuid"),
	}

	recipients := append(splitAddresses(v.To), splitAddresses(v.Cc)...)
	if len(recipients) == 0 {
		v.Error = "At least one recipient is required."
		s.render(w, "compose", v)
		return
	}

	o := outgoing{
		From:       sess.user,
		To:         v.To,
		Cc:         v.Cc,
		Subject:    v.Subject,
		Body:       v.Body,
		InReplyTo:  v.InReplyTo,
		References: v.References,
		Hostname:   s.hostname,
	}
	// Forward-as-attachment embeds the source message verbatim as message/rfc822.
	if v.AttachFolder != "" && v.AttachUID != "" {
		if embed, err := s.loadRaw(sess.mailboxPath, v.AttachFolder, v.AttachUID); err == nil {
			o.Embed = embed
		}
	}
	raw := buildMessage(o)

	unresolved, err := mta.Deliver(s.accounts, recipients, raw, time.Now())
	if err != nil {
		v.Error = "Delivery failed: " + err.Error()
		s.render(w, "compose", v)
		return
	}
	if err := saveToSent(sess.mailboxPath, raw); err != nil {
		v.Error = "Saved no Sent copy: " + err.Error()
		s.render(w, "compose", v)
		return
	}

	// Local recipients are delivered and a Sent copy is stored. If some
	// addresses have no local mailbox, report them (there is no relay yet)
	// rather than pretend they were delivered.
	if len(unresolved) > 0 {
		s.render(w, "compose", composeView{
			Title:  "New message",
			Notice: "Delivered locally and saved to Sent. No local mailbox (and no external relay yet) for: " + strings.Join(unresolved, ", "),
		})
		return
	}
	http.Redirect(w, r, "/mail?folder="+sentName, http.StatusSeeOther)
}

// loadRaw opens the mailbox and returns one message's raw bytes by folder path
// and uid, used to embed a forwarded message at send time.
func (s *Server) loadRaw(mailboxPath, folder, uidStr string) ([]byte, error) {
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(mailboxPath)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	folderID, found := resolveFolder(folders, folder)
	if !found {
		return nil, store.ErrNotFound
	}
	return st.GetMessageRaw(folderID, uint32(uid64))
}

// outgoing is the set of fields buildMessage assembles into an RFC 5322 message.
type outgoing struct {
	From       string
	To         string
	Cc         string
	Subject    string
	Body       string
	InReplyTo  string
	References string
	Embed      []byte // when set, the message is multipart/mixed with this raw embedded as message/rfc822
	Hostname   string
}

// buildMessage assembles an RFC 5322 message from the compose fields. With
// o.Embed set it produces a multipart/mixed message carrying the embedded
// original as a message/rfc822 attachment (forward-as-attachment); otherwise a
// single text/plain body.
func buildMessage(o outgoing) []byte {
	// Normalize the textarea's line endings to CRLF for the wire/store.
	body := strings.ReplaceAll(o.Body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", o.From)
	fmt.Fprintf(&b, "To: %s\r\n", o.To)
	if strings.TrimSpace(o.Cc) != "" {
		fmt.Fprintf(&b, "Cc: %s\r\n", o.Cc)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", stdmime.QEncoding.Encode("utf-8", o.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", randomToken()[:24], o.Hostname)
	if o.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", o.InReplyTo)
	}
	if o.References != "" {
		fmt.Fprintf(&b, "References: %s\r\n", o.References)
	}
	b.WriteString("MIME-Version: 1.0\r\n")

	if len(o.Embed) > 0 {
		boundary := randomToken()[:32]
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
		fmt.Fprintf(&b, "--%s\r\n", boundary)
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(body)
		if !strings.HasSuffix(body, "\r\n") {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "--%s\r\n", boundary)
		b.WriteString("Content-Type: message/rfc822\r\n")
		b.WriteString("Content-Disposition: attachment; filename=\"forwarded.eml\"\r\n\r\n")
		b.Write(o.Embed)
		if !strings.HasSuffix(string(o.Embed), "\r\n") {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "--%s--\r\n", boundary)
		return []byte(b.String())
	}

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
