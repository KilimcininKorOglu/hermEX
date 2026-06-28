package tlsrpt

import (
	"crypto/x509"
	"errors"
)

// Classify maps a TLS handshake error to a TLS-RPT result type (RFC 8460 §4.3).
// A nil error is a successful session and returns "". It recognises the standard
// library's certificate-verification error types and otherwise reports the
// generic validation-failure; the structural cases that are known before a
// handshake (a server that does not offer STARTTLS) are recorded by the caller
// with the explicit result constant, not through this function.
//
// Classification only carries information when verification actually ran: an
// opportunistic STARTTLS session accepts any certificate, so it never produces a
// certificate error and is recorded as a success.
func Classify(err error) string {
	if err == nil {
		return ""
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return ResultCertificateHostMismatch
	}
	if invalid, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		if invalid.Reason == x509.Expired {
			return ResultCertificateExpired
		}
		return ResultValidationFailure
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return ResultCertificateNotTrusted
	}
	return ResultValidationFailure
}
