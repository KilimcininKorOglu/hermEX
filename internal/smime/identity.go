package smime

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// ParseIdentity decrypts a PKCS#12 container with password and returns the
// private key and leaf certificate. It validates an uploaded identity and
// unlocks it per session for signing and decryption. The PKCS#12 may use modern
// AES-based protection (as openssl 3 produces by default), not only the legacy
// algorithms.
func ParseIdentity(p12 []byte, password string) (crypto.PrivateKey, *x509.Certificate, error) {
	key, cert, err := pkcs12.Decode(p12, password)
	if err != nil {
		return nil, nil, fmt.Errorf("smime: parse PKCS#12: %w", err)
	}
	if key == nil || cert == nil {
		return nil, nil, errors.New("smime: PKCS#12 missing a private key or certificate")
	}
	return key, cert, nil
}

// ParseCertificate parses an X.509 certificate from PEM or raw DER, used to
// import a recipient's encryption certificate.
func ParseCertificate(data []byte) (*x509.Certificate, error) {
	der := data
	if block, _ := pem.Decode(data); block != nil {
		der = block.Bytes
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("smime: parse certificate: %w", err)
	}
	return cert, nil
}
