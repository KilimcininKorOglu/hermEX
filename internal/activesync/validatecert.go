package activesync

import (
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"time"

	"hermex/internal/smime"
	"hermex/internal/wbxml"
)

// ValidateCert Status values (MS-ASCMD 2.2.3.166): the overall command status and
// the per-certificate validation result.
const (
	vcStatusOK            = 1 // the certificate validated
	vcStatusProtocolError = 2 // the request named no certificate
	vcStatusCantValidate  = 3 // the certificate could not be parsed/validated
	vcStatusUntrusted     = 4 // the certificate does not chain to a trusted root
	vcStatusNotForSign    = 6 // the certificate is not valid for S/MIME signing
	vcStatusBadTime       = 8 // a certificate in the chain is expired or not yet valid
)

// handleValidateCert answers ValidateCert ([MS-ASCMD] 2.2.2.21): each certificate
// in the request is validated for S/MIME signing — parsed, then verified to chain
// to a trusted root (the server's configured anchors, or the system roots) with
// any supplied CertificateChain certs as intermediates. The reply carries an
// overall status and one per-certificate status. v1 does not perform CRL
// revocation checking (the CheckCRL flag is accepted and ignored).
func (s *Server) handleValidateCert(w http.ResponseWriter, r *http.Request, _ *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	leaves := certElements(root.Child(wbxml.VCCertificates))
	if len(leaves) == 0 {
		writeWBXML(w, wbxml.Elem(wbxml.VCValidateCert,
			wbxml.Str(wbxml.VCStatus, strconv.Itoa(vcStatusProtocolError))))
		return
	}
	intermediates := certPool(certElements(root.Child(wbxml.VCCertificateChain)))

	children := []*wbxml.Node{wbxml.Str(wbxml.VCStatus, strconv.Itoa(vcStatusOK))}
	for _, der := range leaves {
		children = append(children, wbxml.Elem(wbxml.VCCertificate,
			wbxml.Str(wbxml.VCStatus, strconv.Itoa(s.validateCert(der, intermediates)))))
	}
	writeWBXML(w, wbxml.Elem(wbxml.VCValidateCert, children...))
}

// certElements collects the base64-DER bodies of the Certificate children of a
// Certificates or CertificateChain container.
func certElements(container *wbxml.Node) []string {
	if container == nil {
		return nil
	}
	var out []string
	for _, c := range container.Children {
		if c.Tag == wbxml.VCCertificate && c.Text != "" {
			out = append(out, c.Text)
		}
	}
	return out
}

// certPool parses a set of base64-DER certificates into an intermediates pool,
// skipping any that fail to parse.
func certPool(b64 []string) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, s := range b64 {
		der, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			continue
		}
		if cert, err := smime.ParseCertificate(der); err == nil {
			pool.AddCert(cert)
		}
	}
	return pool
}

// validateCert parses one base64-DER certificate and verifies it for S/MIME
// signing, returning its ValidateCert status.
func (s *Server) validateCert(b64 string, intermediates *x509.CertPool) int {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return vcStatusCantValidate
	}
	cert, err := smime.ParseCertificate(der)
	if err != nil {
		return vcStatusCantValidate
	}
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:         s.roots, // nil = system roots
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		CurrentTime:   time.Now(),
	})
	return verifyStatus(err)
}

// verifyStatus maps an x509 chain-verification error to a ValidateCert status.
func verifyStatus(err error) int {
	if err == nil {
		return vcStatusOK
	}
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		return vcStatusUntrusted
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		switch invalid.Reason {
		case x509.Expired:
			return vcStatusBadTime
		case x509.IncompatibleUsage:
			return vcStatusNotForSign
		}
	}
	return vcStatusCantValidate
}
