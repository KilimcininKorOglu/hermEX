package webmail

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/smime"
)

// applySmime turns a built RFC 5322 message into a signed and/or encrypted S/MIME
// message. Signing requires the session's unlocked identity; encrypting requires
// a stored certificate for every recipient. When both are requested the message
// is signed first and the signed entity is then enveloped (sign-then-encrypt). It
// returns raw unchanged when neither is requested.
func (s *Server) applySmime(sess *session, st *objectstore.Store, raw []byte, recipients []string, sign, encrypt bool) ([]byte, error) {
	if !sign && !encrypt {
		return raw, nil
	}
	identity, inner := splitForSmime(raw)

	if sign {
		if sess.smimeKey == nil || sess.smimeCert == nil {
			return nil, errors.New("unlock your S/MIME certificate first (Certificates page)")
		}
		signed, err := smime.Sign(inner, sess.smimeCert, sess.smimeKey)
		if err != nil {
			s.smimeEvent(logging.LevelWarn, sess.user, "smime.sign", err.Error(), nil)
			return nil, err
		}
		s.smimeEvent(logging.LevelInfo, sess.user, "smime.sign", "", nil)
		if !encrypt {
			return spliceIdentity(identity, signed), nil
		}
		inner = signed // sign-then-encrypt: the signed entity becomes the enveloped content
	}

	certs, err := s.recipientCertsFor(st, recipients)
	if err != nil {
		return nil, err
	}
	// Encrypt to the sender's own certificate too, so the Sent copy stays readable.
	if sess.smimeCert != nil {
		certs = append(certs, sess.smimeCert)
	}
	env, err := smime.Encrypt(inner, certs)
	if err != nil {
		s.smimeEvent(logging.LevelWarn, sess.user, "smime.encrypt", err.Error(), nil)
		return nil, err
	}
	s.smimeEvent(logging.LevelInfo, sess.user, "smime.encrypt", "", logging.Fields{"recipients": len(certs)})
	return spliceIdentity(identity, env), nil
}

// recipientCertsFor collects the stored encryption certificate for each distinct
// recipient address, failing if any recipient has none (so the user is told who
// cannot be sent encrypted mail rather than silently dropping them).
func (s *Server) recipientCertsFor(st *objectstore.Store, recipients []string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	var missing []string
	seen := map[string]bool{}
	for _, r := range recipients {
		addr := strings.ToLower(strings.TrimSpace(bareAddress(r)))
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		der, ok, err := st.GetRecipientCert(addr)
		if err != nil {
			return nil, err
		}
		cert, perr := parseDERCert(der, ok)
		if cert == nil {
			missing = append(missing, addr)
			continue
		}
		if perr != nil {
			return nil, perr
		}
		certs = append(certs, cert)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("no encryption certificate for: %s", strings.Join(missing, ", "))
	}
	return certs, nil
}

// parseDERCert parses a stored DER certificate; it returns (nil, nil) when the
// address had no certificate (ok is false), so the caller reports it as missing.
func parseDERCert(der []byte, ok bool) (*x509.Certificate, error) {
	if !ok {
		return nil, nil
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return cert, nil
}

// splitForSmime divides an RFC 5322 message into its identity headers (From, To,
// Subject, Date, Message-ID — everything that stays on the outer message) and the
// inner MIME entity (the Content-* headers and the body) that S/MIME signs or
// encrypts. MIME-Version is dropped from both: the S/MIME wrapper emits its own.
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
			// dropped; the S/MIME entity supplies its own
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

// spliceIdentity joins outer identity headers with an S/MIME entity (which begins
// with its own Content-Type header), producing the final message.
func spliceIdentity(identity, smimeBlock []byte) []byte {
	out := make([]byte, 0, len(identity)+len(smimeBlock))
	out = append(out, identity...)
	return append(out, smimeBlock...)
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
