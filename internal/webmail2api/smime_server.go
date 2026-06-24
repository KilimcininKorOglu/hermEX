package webmail2api

import (
	"bytes"
	"crypto"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"hermex/internal/objectstore"
	"hermex/internal/smime"
)

// Server-mode S/MIME: when a user keeps their key on the server (Mode "server"),
// the server signs/encrypts outbound mail and decrypts/verifies inbound mail with
// it, the key encrypted at rest under a server-derived password. Browser-mode
// users do all of this in the browser and the server never holds their key.

// smimeStatus describes a received message's S/MIME state for the reader.
type smimeStatus struct {
	Signed    bool
	Encrypted bool
	Verified  bool
	SignedBy  string
}

// smimeP12Password derives the at-rest PKCS#12 password from the server secret,
// so a server-mode key is never stored in plaintext.
func smimeP12Password(secret []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("smime-p12-at-rest-v1"))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// unlockSmimeIdentity returns a server-mode caller's stored key and certificate,
// or ok=false when the mode is not server or it cannot be decrypted.
func unlockSmimeIdentity(st *objectstore.Store, secret []byte) (crypto.PrivateKey, *x509.Certificate, bool) {
	id, ok, err := st.GetSmimeIdentity()
	if err != nil || !ok || id.Mode != "server" || len(id.P12) == 0 {
		return nil, nil, false
	}
	key, cert, err := smime.ParseIdentity(id.P12, smimeP12Password(secret))
	if err != nil {
		return nil, nil, false
	}
	return key, cert, true
}

// isServerMode reports whether the store's owner keeps their S/MIME key server-side.
func isServerMode(st *objectstore.Store) bool {
	id, ok, err := st.GetSmimeIdentity()
	return err == nil && ok && id.Mode == "server"
}

// recipientCert resolves a recipient's published public certificate (any user's
// published cert is readable, as it is meant for encrypting to them).
func (s *Server) recipientCert(addr string) (*x509.Certificate, bool) {
	maildir, ok := s.accounts.Resolve(addr)
	if !ok {
		return nil, false
	}
	st, err := objectstore.Open(maildir)
	if err != nil {
		return nil, false
	}
	defer st.Close()
	id, ok, err := st.GetSmimeIdentity()
	if err != nil || !ok || len(id.Cert) == 0 {
		return nil, false
	}
	cert, err := x509.ParseCertificate(id.Cert)
	if err != nil {
		return nil, false
	}
	return cert, true
}

// applySmime signs and/or encrypts a built message for a server-mode sender. The
// identity headers are split off and re-attached around the S/MIME entity.
func (s *Server) applySmime(mailbox string, raw []byte, recipients []string, sign, encrypt bool) ([]byte, error) {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil, errors.New("mailbox unavailable")
	}
	defer st.Close()
	identity, inner := splitForSmime(raw)
	if sign {
		key, cert, ok := unlockSmimeIdentity(st, s.secret)
		if !ok {
			return nil, errors.New("no server-side S/MIME identity is set up")
		}
		signed, err := smime.Sign(inner, cert, key)
		if err != nil {
			return nil, fmt.Errorf("could not sign the message: %w", err)
		}
		if !encrypt {
			return append(identity, signed...), nil
		}
		inner = signed
	}
	certs := make([]*x509.Certificate, 0, len(recipients)+1)
	for _, addr := range recipients {
		cert, ok := s.recipientCert(strings.ToLower(strings.TrimSpace(addr)))
		if !ok {
			return nil, fmt.Errorf("no S/MIME certificate published for %s, so the message cannot be encrypted", addr)
		}
		certs = append(certs, cert)
	}
	// Encrypt to the sender's own certificate too, so the Sent copy stays readable.
	if _, ownCert, ok := unlockSmimeIdentity(st, s.secret); ok {
		certs = append(certs, ownCert)
	}
	env, err := smime.Encrypt(inner, certs)
	if err != nil {
		return nil, fmt.Errorf("could not encrypt the message: %w", err)
	}
	return append(identity, env...), nil
}

// smimeOpen decrypts (with the server-mode reader's key) then verifies a received
// message, returning the inner content and its status.
func (s *Server) smimeOpen(st *objectstore.Store, raw []byte) ([]byte, smimeStatus) {
	var status smimeStatus
	content := raw
	if smime.IsEncrypted(content) {
		status.Encrypted = true
		if key, cert, ok := unlockSmimeIdentity(st, s.secret); ok {
			if dec, err := smime.Decrypt(content, cert, key); err == nil {
				content = dec
			}
		}
	}
	if smime.IsSigned(content) {
		status.Signed = true
		if signer, _, err := smime.Verify(content); err == nil {
			status.Verified = true
			status.SignedBy = certEmail(signer)
		}
	}
	return content, status
}

// splitForSmime divides an RFC 5322 message into its identity headers (stays on
// the outer message) and the inner MIME entity (signed or encrypted).
// MIME-Version is dropped from both: the S/MIME wrapper emits its own.
func splitForSmime(raw []byte) (identityHeaders, innerEntity []byte) {
	hdr, body, found := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !found {
		return raw, nil
	}
	var ident, content bytes.Buffer
	for _, line := range splitHeaderLines(hdr) {
		name := strings.ToLower(headerName(line))
		switch {
		case name == "mime-version":
		case strings.HasPrefix(name, "content-"):
			content.Write(line)
			content.WriteString("\r\n")
		default:
			ident.Write(line)
			ident.WriteString("\r\n")
		}
	}
	content.WriteString("\r\n")
	content.Write(body)
	return ident.Bytes(), content.Bytes()
}

// splitHeaderLines splits a CRLF header block into logical headers, keeping each
// folded continuation joined to its header line.
func splitHeaderLines(hdr []byte) [][]byte {
	lines := strings.Split(string(hdr), "\r\n")
	var out [][]byte
	for _, l := range lines {
		if l != "" && (l[0] == ' ' || l[0] == '\t') && len(out) > 0 {
			out[len(out)-1] = append(out[len(out)-1], append([]byte("\r\n"), l...)...)
			continue
		}
		out = append(out, []byte(l))
	}
	return out
}

// headerName returns the field name of a header line (the text before the colon).
func headerName(line []byte) string {
	name, _, found := bytes.Cut(line, []byte{':'})
	if !found {
		return ""
	}
	return string(bytes.TrimSpace(name))
}
