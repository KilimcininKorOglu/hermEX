package arc

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// LookupTXT resolves the TXT records of a DNS name. The caller supplies one that
// carries a deadline, because the name is chosen by whoever sealed the chain.
type LookupTXT func(name string) ([]string, error)

// algorithm names the signing algorithm of a key, as the a= tag writes it.
func algorithm(key crypto.Signer) (string, error) {
	switch key.Public().(type) {
	case *rsa.PublicKey:
		return "rsa-sha256", nil
	case ed25519.PublicKey:
		return "ed25519-sha256", nil
	}
	return "", errors.New("arc: unsupported key type")
}

// sign signs data with key: RSA signs the SHA-256 digest with PKCS #1 v1.5, and
// Ed25519 signs the digest itself (RFC 8463 section 3).
func sign(key crypto.Signer, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	opts := crypto.SignerOpts(crypto.SHA256)
	if _, ok := key.Public().(ed25519.PublicKey); ok {
		opts = crypto.Hash(0)
	}
	sig, err := key.Sign(rand.Reader, sum[:], opts)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// verify checks a base64 signature over data with a public key and the
// algorithm the a= tag named.
func verify(pub crypto.PublicKey, algo, sigB64 string, data []byte) error {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("arc: signature is not base64: %w", err)
	}
	sum := sha256.Sum256(data)
	switch k := pub.(type) {
	case *rsa.PublicKey:
		if algo != "rsa-sha256" {
			return errors.New("arc: algorithm does not match the key")
		}
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], sig)
	case ed25519.PublicKey:
		if algo != "ed25519-sha256" || !ed25519.Verify(k, sum[:], sig) {
			return errors.New("arc: ed25519 signature does not verify")
		}
		return nil
	}
	return errors.New("arc: unsupported key type")
}

// publicKey fetches and parses the key a selector publishes for a domain
// (RFC 6376 section 3.6.2). A revoked key (empty p=) is an error.
func publicKey(lookup LookupTXT, selector, domain string) (crypto.PublicKey, error) {
	txts, err := lookup(selector + "._domainkey." + domain)
	if err != nil {
		return nil, err
	}
	tags := tagList(strings.Join(txts, ""))
	der, err := base64.StdEncoding.DecodeString(tags["p"])
	if err != nil || len(der) == 0 {
		return nil, errors.New("arc: the key record has no usable p=")
	}
	if tags["k"] == "ed25519" {
		if len(der) != ed25519.PublicKeySize {
			return nil, errors.New("arc: the ed25519 key has the wrong size")
		}
		return ed25519.PublicKey(der), nil
	}
	if pub, err := x509.ParsePKIXPublicKey(der); err == nil {
		return pub, nil
	}
	return x509.ParsePKCS1PublicKey(der)
}
