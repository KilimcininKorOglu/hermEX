// Package dkimsign signs hermEX's outbound mail with the sending domain's DKIM key so
// it passes DKIM (and DMARC alignment) at receivers instead of being treated as
// unauthenticated. It is the signing counterpart to the inbound DKIM verification, and
// is deliberately daemon-neutral: it takes a KeyProvider interface (the directory
// implements it) rather than importing the directory, so both the MTA and webmail can
// install the same signer on their relay spool.
//
// Signing fails OPEN: any problem, no key for the domain, a malformed key, an
// unparseable From header, a library error, yields the original message unsigned. A
// message must never be blocked or delayed because signing broke.
package dkimsign

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/emersion/go-msgauth/dkim"

	"hermex/internal/logging"
)

// signedHeaders is the curated set of header fields covered by the signature. Volatile,
// hop-added fields (Received, Return-Path) are deliberately excluded: the receiving MX
// prepends them after we sign, and a field absent from h= leaves the signature intact.
var signedHeaders = []string{
	"From", "To", "Cc", "Subject", "Date", "Message-ID", "Reply-To",
	"In-Reply-To", "References", "MIME-Version", "Content-Type", "Content-Transfer-Encoding",
}

// KeyProvider supplies the DKIM signing material for a domain. The directory implements
// it against the stored per-domain keys.
type KeyProvider interface {
	// DKIMKey returns the PEM-encoded private key and selector of a domain's ENABLED
	// signing key. found is false when the domain has no enabled key; only an enabled
	// key is ever returned, so a key whose DNS record is not yet published does not
	// start producing DKIM=fail.
	DKIMKey(domain string) (privPEM []byte, selector string, found bool, err error)
}

// Signer prepends a DKIM-Signature to outbound mail using the From-header domain's key.
type Signer struct {
	Keys   KeyProvider
	Logger *logging.Logger
}

// Sign returns body with a DKIM-Signature for the From-header domain prepended, or the
// original body unchanged when no enabled key exists or anything goes wrong. The
// signing domain is taken from the From header (not the envelope sender) so d= aligns
// with the address receivers check for DMARC.
func (s *Signer) Sign(body []byte) []byte {
	domain := fromHeaderDomain(body)
	if domain == "" {
		return body
	}
	privPEM, selector, found, err := s.Keys.DKIMKey(domain)
	if err != nil {
		s.logErr(domain, "lookup", err)
		return body
	}
	if !found {
		return body
	}
	key, err := parsePrivateKey(privPEM)
	if err != nil {
		s.logErr(domain, "key", err)
		return body
	}
	var out bytes.Buffer
	opts := &dkim.SignOptions{
		Domain:                 domain,
		Selector:               selector,
		Signer:                 key,
		Hash:                   crypto.SHA256,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		HeaderKeys:             signedHeaders,
	}
	if err := dkim.Sign(&out, bytes.NewReader(body), opts); err != nil {
		s.logErr(domain, "sign", err)
		return body
	}
	return out.Bytes()
}

// logErr records a signing failure when a logger is configured. A missing key is not an
// error and is never logged.
func (s *Signer) logErr(domain, stage string, err error) {
	if s.Logger == nil {
		return
	}
	s.Logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "dkim.sign.error",
		Fields: logging.Fields{"domain": domain, "stage": stage}, Err: err.Error()})
}

// fromHeaderDomain returns the lower-cased domain of the first From-header address, or
// "" when the header is missing or unparseable.
func fromHeaderDomain(body []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	addrs, err := mail.ParseAddressList(msg.Header.Get("From"))
	if err != nil || len(addrs) == 0 {
		return ""
	}
	at := strings.LastIndexByte(addrs[0].Address, '@')
	if at < 0 || at == len(addrs[0].Address)-1 {
		return ""
	}
	return strings.ToLower(addrs[0].Address[at+1:])
}

// parsePrivateKey decodes a PEM private key (PKCS#1 or PKCS#8) into a crypto.Signer.
func parsePrivateKey(privPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(privPEM)
	if block == nil {
		return nil, errors.New("dkimsign: no PEM block in private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("dkimsign: private key is not a signer")
	}
	return signer, nil
}

// The DKIM key algorithms hermEX can generate, named as they appear in the k= tag of the
// published TXT record (RFC 6376 section 3.6.1). RSA is the interoperable default; Ed25519
// (RFC 8463) produces a far shorter record but is not yet verified by every receiver, so
// it is a deliberate operator choice rather than the default.
const (
	KeyRSA     = "rsa"
	KeyEd25519 = "ed25519"
)

// GenerateKey creates a fresh DKIM keypair of the given algorithm (KeyRSA when empty) and
// returns the PEM-encoded private key to store and the TXT record value to publish at
// {selector}._domainkey.{domain}. Generating a key does not enable signing: the operator
// publishes the record, then enables the key as a separate step.
func GenerateKey(keyType string) (privPEM []byte, dnsTXT string, err error) {
	switch keyType {
	case "", KeyRSA:
		return generateRSAKey()
	case KeyEd25519:
		return generateEd25519Key()
	default:
		return nil, "", fmt.Errorf("dkimsign: unsupported key type %q", keyType)
	}
}

// generateRSAKey mints an RSA-2048 keypair. The public key travels in the record as a
// base64 PKIX DER structure, which is what a verifier parses for k=rsa.
func generateRSAKey() (privPEM []byte, dnsTXT string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	privPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, "", err
	}
	return privPEM, "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(pubDER), nil
}

// generateEd25519Key mints an Ed25519 keypair. Unlike RSA, RFC 8463 puts the RAW 32-byte
// public key in p=, not a DER structure, and a verifier rejects any other length.
func generateEd25519Key() (privPEM []byte, dnsTXT string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, "", err
	}
	privPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	return privPEM, "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub), nil
}

// PublicKeyPayload returns the p= value of a generated TXT record, the base64 public key
// on its own. It parses what GenerateKey produced, so the record grammar stays in one
// package. An empty string means the record carries no p= tag.
func PublicKeyPayload(dnsTXT string) string {
	for tag := range strings.SplitSeq(dnsTXT, ";") {
		tag = strings.TrimSpace(tag)
		if after, ok := strings.CutPrefix(tag, "p="); ok {
			return after
		}
	}
	return ""
}
