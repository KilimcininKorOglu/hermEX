package webmail2api

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/smime"
)

// An S/MIME signature proves only that whoever holds the signing key produced the
// bytes. It says nothing about who that is: anyone can mint a self-signed
// certificate carrying any address in its SAN and sign a message with it. The
// reader's "verified" badge is a claim about identity, so it is earned here, by
// binding the signer to the message's own From address and then to something the
// server already trusts, and never by the signature check alone.

// certAssertsAddress reports whether cert claims the given mailbox address, in the
// SAN rfc822Name entries or, for older certificates that carry it nowhere else, in
// the subject common name. Addresses compare case-insensitively: the domain is by
// definition, and no mail system in practice treats two local parts differing only
// in case as different people.
func certAssertsAddress(cert *x509.Certificate, address string) bool {
	address = strings.TrimSpace(address)
	if cert == nil || address == "" {
		return false
	}
	for _, san := range cert.EmailAddresses {
		if strings.EqualFold(strings.TrimSpace(san), address) {
			return true
		}
	}
	// Only a common name that is itself an address can assert one; a display-name
	// CN ("Alice Example") must never satisfy an address match.
	cn := strings.TrimSpace(cert.Subject.CommonName)
	return strings.Contains(cn, "@") && strings.EqualFold(cn, address)
}

// publishedCert returns the certificate a local user published for their own
// address (the same record handleRecipientCert serves to senders who want to
// encrypt to them). ok is false when the address is not local, its mailbox cannot
// be opened, or nothing is published.
func (s *Server) publishedCert(address string) (der []byte, ok bool) {
	maildir, ok := s.accounts.Resolve(address)
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
	return id.Cert, true
}

// signerTrusted reports whether the signer of sig may be presented to the reader
// as the message's sender. Both halves are required: the certificate must assert
// from, and it must be trusted by one of two routes.
//
// Pinning covers internal mail, where an organization commonly runs its own
// self-signed certificates: the signer must be byte-identical to the certificate
// that local user published for themselves, which only they can set (it is written
// through their own authenticated session). Chain validation covers external mail:
// the certificate must build a path to a system root, with the certificates the
// message itself carried offered as intermediates.
//
// A certificate outside its validity window is refused on both routes: x509.Verify
// enforces it for the chain, and the pinned route checks it directly.
func (s *Server) signerTrusted(sig smime.Signature, from string) bool {
	return s.certTrustedFor(sig.Signer, sig.Certs, from)
}

// certTrustedFor is the trust decision itself, over a leaf and whatever
// intermediates accompanied it. It is shared by the server-side read path and by
// the endpoint the browser-mode reader asks, so one policy answers both.
func (s *Server) certTrustedFor(signer *x509.Certificate, carried []*x509.Certificate, from string) bool {
	if signer == nil || !certAssertsAddress(signer, from) {
		return false
	}
	now := time.Now()
	if der, ok := s.publishedCert(from); ok && bytes.Equal(der, signer.Raw) {
		return !now.Before(signer.NotBefore) && !now.After(signer.NotAfter)
	}
	intermediates := x509.NewCertPool()
	for _, c := range carried {
		if c != nil && !c.Equal(signer) {
			intermediates.AddCert(c)
		}
	}
	_, err := signer.Verify(x509.VerifyOptions{
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		CurrentTime:   now,
	})
	return err == nil
}

// smimeStatusFor verifies a signed message and reports what the reader may be
// told. verified is true only for a signature that is both mathematically valid
// and attributable to from; signedBy carries the address the certificate claims
// either way, so an unverified signature can still be shown with the identity it
// asserted (the reader's banner labels it as unverified).
func (s *Server) smimeStatusFor(raw []byte, from string) (verified bool, signedBy string) {
	sig, err := smime.Verify(raw)
	if err != nil {
		return false, ""
	}
	return s.signerTrusted(sig, from), certEmail(sig.Signer)
}

// handleVerifySmimeSigner answers whether a signer certificate may be attributed
// to a sender. A browser-mode reader decrypts and signature-checks its mail
// locally (posting the decrypted content would leak the plaintext), but the
// certificate is public, so the trust half of the decision is asked here instead
// of being reimplemented in the client: one policy, in tested Go. The caller
// already proved the signature; this endpoint deliberately answers only the
// identity-and-trust question and never reports a signature as valid.
func (s *Server) handleVerifySmimeSigner(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		Cert string `json:"cert"` // the signer's certificate, base64 DER or PEM
		From string `json:"from"` // the address the message claims to be from
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.Cert))
	if err != nil {
		der, err = parseCertDER([]byte(req.Cert))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid certificate"})
			return
		}
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid certificate"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"trusted": s.certTrustedFor(cert, nil, req.From)})
}
