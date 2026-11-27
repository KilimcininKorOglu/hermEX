package directory

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"
)

// wrapPrefix tags a stored key as wrapped. A value without it is a plaintext row
// written before a secret was configured, and is returned as it is: the format is
// self-describing, so adopting encryption needs no schema change and no migration.
const wrapPrefix = "enc:v1:"

// Wrapping contexts. They bind a ciphertext to the column it belongs in, so a
// value moved from one key column to another fails to open rather than being
// silently accepted as the other kind of key.
const (
	wrapDKIM    = "dkim-private-key"
	wrapTLS     = "tls-private-key"
	wrapGateway = "smtp-gateway-password"
)

// SetKeySecret installs the at-rest wrapping secret for the private keys this
// directory stores (DKIM signing keys, uploaded TLS keys). An empty secret leaves
// them in plaintext, exactly as before, so a deployment that has not configured
// one keeps working; the daemons report that state at startup.
//
// The database is the only copy of these keys, so a dump or a database-level
// compromise hands them over in immediately usable form. Wrapping them moves the
// secret out of the dump and into the configuration file.
func (d *SQLDirectory) SetKeySecret(secret []byte) {
	if len(secret) == 0 {
		// Said out loud, once per daemon start: an operator who has never set the
		// secret would otherwise have no signal that the database holds usable
		// signing and serving keys.
		log.Print("directory: no key secret configured, DKIM and TLS private keys are stored unencrypted (set key_secret)")
		d.keySecret.Store(nil)
		return
	}
	// Derive rather than use the secret directly: the same shape the S/MIME
	// at-rest wrapping uses, so one configured secret can key several purposes
	// without any of them sharing material.
	key := hmacSHA256(secret, []byte("hermex.directory.keywrap.v1"))
	d.keySecret.Store(&key)
}

// hmacSHA256 computes RFC 2104 HMAC-SHA256 using sha256.Sum256 so key derivation
// has no io.Writer error to discard.
func hmacSHA256(key, msg []byte) []byte {
	var padded [sha256.BlockSize]byte
	if len(key) > sha256.BlockSize {
		sum := sha256.Sum256(key)
		copy(padded[:], sum[:])
	} else {
		copy(padded[:], key)
	}
	in := make([]byte, sha256.BlockSize+len(msg))
	for i := range padded {
		in[i] = padded[i] ^ 0x36
	}
	copy(in[sha256.BlockSize:], msg)
	inner := sha256.Sum256(in)

	var outBuf [sha256.BlockSize + sha256.Size]byte
	for i := range padded {
		outBuf[i] = padded[i] ^ 0x5c
	}
	copy(outBuf[sha256.BlockSize:], inner[:])
	outer := sha256.Sum256(outBuf[:])
	return outer[:]
}

// hasKeySecret reports whether an at-rest wrapping secret is currently installed.
func (d *SQLDirectory) hasKeySecret() bool {
	return d.keySecret.Load() != nil
}

// wrapping returns the AEAD for the installed secret, or ok=false when none is.
func (d *SQLDirectory) wrapping() (aead cipher.AEAD, ok bool) {
	key := d.keySecret.Load()
	if key == nil {
		return nil, false
	}
	block, err := aes.NewCipher(*key)
	if err != nil {
		return nil, false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, false
	}
	return gcm, true
}

// wrapKey encrypts a PEM key for storage under the given context. With no secret
// installed it returns the input unchanged.
func (d *SQLDirectory) wrapKey(context, pem string) (string, error) {
	gcm, ok := d.wrapping()
	if !ok || pem == "" {
		return pem, nil
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(pem), []byte(context))
	return wrapPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// unwrappedKey is the result of unwrapKey: the plaintext key material and
// whether the stored database value was already wrapped.
type unwrappedKey struct {
	plain   string
	wrapped bool
}

// unwrapKey returns the PEM behind a stored value. A value with no wrap prefix is
// a plaintext row and is returned as it is, with wrapped=false so the caller can
// rewrite it. A wrapped value that will not open is an error: reporting it as
// plaintext would hand the caller a key-shaped string that is not a key.
func (d *SQLDirectory) unwrapKey(context, stored string) (unwrappedKey, error) {
	if !strings.HasPrefix(stored, wrapPrefix) {
		return unwrappedKey{plain: stored}, nil
	}
	gcm, ok := d.wrapping()
	if !ok {
		return unwrappedKey{wrapped: true}, errors.New("directory: a stored private key is encrypted but no key secret is configured")
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, wrapPrefix))
	if err != nil {
		return unwrappedKey{wrapped: true}, fmt.Errorf("directory: stored private key is malformed: %w", err)
	}
	if len(sealed) < gcm.NonceSize() {
		return unwrappedKey{wrapped: true}, errors.New("directory: stored private key is truncated")
	}
	nonce, ct := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, []byte(context))
	if err != nil {
		return unwrappedKey{wrapped: true}, errors.New("directory: a stored private key could not be decrypted with the configured key secret")
	}
	return unwrappedKey{plain: string(plain), wrapped: true}, nil
}

// rewrapNeeded reports whether a value just read should be written back wrapped:
// a secret is installed and the row is still plaintext. The rewrite is what makes
// an existing deployment converge on encryption with nothing to run.
func (d *SQLDirectory) rewrapNeeded(wrapped bool) bool {
	return !wrapped && d.hasKeySecret()
}
