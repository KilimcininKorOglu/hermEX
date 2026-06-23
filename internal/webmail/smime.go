package webmail

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"net/http"

	"hermex/internal/objectstore"
	"hermex/internal/smime"
)

// smimePage is the data the S/MIME settings template renders: the stored
// identity's certificate details (read from the public cert, no passphrase
// needed), whether it is unlocked this session, and the recipient certificate
// list used for encryption.
type smimePage struct {
	User        string
	HasIdentity bool
	Unlocked    bool
	Subject     string
	Issuer      string
	Expires     string
	Recipients  []recipientCertView
	Error       string
	Notice      string
}

// recipientCertView is one stored recipient certificate row.
type recipientCertView struct {
	Address string
	Subject string
	Expires string
}

// handleSmimeForm redirects the former standalone S/MIME page to its tab on the
// unified settings page; the POST endpoint below still serves the forms.
func (s *Server) handleSmimeForm(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings?tab=smime", http.StatusSeeOther)
}

// smimeNotice maps an S/MIME success code carried on the post-redirect to its
// message, shown when the settings page reopens on the certificates tab.
func smimeNotice(code string) string {
	switch code {
	case "uploaded":
		return "Certificate uploaded and unlocked for this session."
	case "unlocked":
		return "Identity unlocked for this session."
	case "removed":
		return "Certificate removed."
	case "recipient":
		return "Recipient certificate saved."
	case "recipientremoved":
		return "Recipient certificate removed."
	}
	return ""
}

// buildSmimePage assembles the S/MIME page state from the store and session.
func (s *Server) buildSmimePage(st *objectstore.Store, sess *session) smimePage {
	page := smimePage{User: sess.user, Unlocked: sess.smimeKey != nil}
	if id, ok, err := st.GetSmimeIdentity(); err == nil && ok {
		page.HasIdentity = true
		if cert, err := x509.ParseCertificate(id.Cert); err == nil {
			page.Subject = certName(cert.Subject)
			page.Issuer = certName(cert.Issuer)
			page.Expires = cert.NotAfter.Format("2006-01-02")
		}
	}
	if certs, err := st.ListRecipientCerts(); err == nil {
		for addr, der := range certs {
			v := recipientCertView{Address: addr}
			if cert, err := x509.ParseCertificate(der); err == nil {
				v.Subject = certName(cert.Subject)
				v.Expires = cert.NotAfter.Format("2006-01-02")
			}
			page.Recipients = append(page.Recipients, v)
		}
		sortRecipients(page.Recipients)
	}
	return page
}

func (s *Server) handleSmimeSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	token, _ := r.Cookie(sessionCookie)
	r.Body = http.MaxBytesReader(w, r.Body, maxComposeBytes)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		s.smimeError(w, sess, "The upload failed or was too large.")
		return
	}
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()

	switch r.FormValue("action") {
	case "upload":
		p12 := readFormFile(r, "p12")
		if len(p12) == 0 {
			s.smimeError(w, sess, "Choose a PKCS#12 (.p12 / .pfx) file to upload.")
			return
		}
		key, cert, err := smime.ParseIdentity(p12, r.FormValue("passphrase"))
		if err != nil {
			s.smimeError(w, sess, "Could not open the file — wrong passphrase or unsupported format.")
			return
		}
		if err := st.SetSmimeIdentity(objectstore.SmimeIdentity{P12: p12, Cert: cert.Raw}); err != nil {
			s.smimeError(w, sess, "Could not save the certificate: "+err.Error())
			return
		}
		if token != nil {
			s.sessions.unlockSmime(token.Value, key, cert)
		}
		http.Redirect(w, r, "/settings?tab=smime&ok=uploaded", http.StatusSeeOther)
	case "unlock":
		id, ok, err := st.GetSmimeIdentity()
		if err != nil || !ok {
			s.smimeError(w, sess, "There is no certificate to unlock.")
			return
		}
		key, cert, err := smime.ParseIdentity(id.P12, r.FormValue("passphrase"))
		if err != nil {
			s.smimeError(w, sess, "Wrong passphrase.")
			return
		}
		if token != nil {
			s.sessions.unlockSmime(token.Value, key, cert)
		}
		http.Redirect(w, r, "/settings?tab=smime&ok=unlocked", http.StatusSeeOther)
	case "remove":
		if err := st.ClearSmimeIdentity(); err != nil {
			s.smimeError(w, sess, "Could not remove the certificate: "+err.Error())
			return
		}
		if token != nil {
			s.sessions.lockSmime(token.Value)
		}
		http.Redirect(w, r, "/settings?tab=smime&ok=removed", http.StatusSeeOther)
	case "addrecipient":
		addr := r.FormValue("address")
		cert := readFormFile(r, "cert")
		parsed, err := smime.ParseCertificate(cert)
		if addr == "" || err != nil {
			s.smimeError(w, sess, "Enter an address and choose a valid certificate (PEM or DER).")
			return
		}
		if err := st.PutRecipientCert(addr, parsed.Raw); err != nil {
			s.smimeError(w, sess, "Could not save the recipient certificate: "+err.Error())
			return
		}
		http.Redirect(w, r, "/settings?tab=smime&ok=recipient", http.StatusSeeOther)
	case "removerecipient":
		if err := st.DeleteRecipientCert(r.FormValue("address")); err != nil {
			s.smimeError(w, sess, "Could not remove the recipient certificate: "+err.Error())
			return
		}
		http.Redirect(w, r, "/settings?tab=smime&ok=recipientremoved", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/settings?tab=smime", http.StatusSeeOther)
	}
}

// smimeError re-renders the whole settings page with the S/MIME tab active and an
// error message, keeping the user on the unified page with the certificates
// section showing the problem. The message is free text (it can include a store
// error), so it cannot ride a 303 — this renders directly with a 400.
func (s *Server) smimeError(w http.ResponseWriter, sess *session, msg string) {
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()
	page := s.buildSettingsPage(sess, st, "smime")
	page.Smime.Error = msg
	w.WriteHeader(http.StatusBadRequest)
	s.render(w, "settings", page)
}

// readFormFile returns the bytes of the first uploaded file for a multipart form
// field, or nil when absent or unreadable.
func readFormFile(r *http.Request, field string) []byte {
	if r.MultipartForm == nil {
		return nil
	}
	fhs := r.MultipartForm.File[field]
	if len(fhs) == 0 {
		return nil
	}
	f, err := fhs[0].Open()
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	return data
}

// certName returns a certificate name's common name, or its full RFC 2253 string
// when there is no CN.
func certName(n pkix.Name) string {
	if n.CommonName != "" {
		return n.CommonName
	}
	return n.String()
}

// sortRecipients orders recipient rows by address for a stable display.
func sortRecipients(rs []recipientCertView) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].Address > rs[j].Address; j-- {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}
