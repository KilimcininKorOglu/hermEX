package tlsrpt

import (
	"crypto/x509"
	"errors"
	"fmt"
	"testing"
)

// TestClassify proves each standard-library certificate error maps to the
// matching RFC 8460 result type, a nil error is a success ("") and an unrelated
// error degrades to the generic validation-failure rather than a wrong specific
// type. The errors are wrapped to prove the classifier unwraps (mirroring how
// crypto/tls surfaces a verification failure).
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, ""},
		{"host mismatch", x509.HostnameError{Host: "mx.example.com"}, ResultCertificateHostMismatch},
		{"expired", x509.CertificateInvalidError{Reason: x509.Expired}, ResultCertificateExpired},
		{"other invalid", x509.CertificateInvalidError{Reason: x509.NotAuthorizedToSign}, ResultValidationFailure},
		{"untrusted", x509.UnknownAuthorityError{}, ResultCertificateNotTrusted},
		{"wrapped untrusted", fmt.Errorf("tls: %w", x509.UnknownAuthorityError{}), ResultCertificateNotTrusted},
		{"unrelated", errors.New("connection reset"), ResultValidationFailure},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.err); got != c.want {
				t.Errorf("Classify(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}
