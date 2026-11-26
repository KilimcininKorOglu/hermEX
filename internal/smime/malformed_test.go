package smime

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"testing"
)

// TestVerifyRefusesATruncatedSignature hands Verify signed mail whose signature
// part is cut inside its BER length field: 0x30 0x82 0x01 declares a two-byte
// length (X.690 8.1.3.5) and supplies one. The message comes from any sender, so
// the parser must answer with an error, not run off the end of the buffer.
func TestVerifyRefusesATruncatedSignature(t *testing.T) {
	for _, sig := range [][]byte{{0x30, 0x82, 0x01}, {0x30, 0x84, 0x00}, {0x30, 0x81}, {0x30}} {
		raw := "Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; boundary=b\r\n\r\n" +
			"--b\r\n\r\nx\r\n--b\r\nContent-Type: application/pkcs7-signature\r\n" +
			"Content-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(sig) + "\r\n--b--\r\n"
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("signature % X: Verify panicked: %v", sig, r)
				}
			}()
			if _, err := Verify([]byte(raw)); err == nil {
				t.Errorf("signature % X: a truncated signature verified", sig)
			}
		}()
	}
}

// TestDecryptRefusesBadPadding alters the last byte of the ciphertext of mail
// encrypted to the reader. That byte decides the CBC padding length of the last
// block (RFC 5652 section 6.3), and anyone who can send mail can set it, since
// the recipient's certificate is public. Every value must come back as an error:
// a padding length longer than the content must not stop the request that reads
// the message.
func TestDecryptRefusesBadPadding(t *testing.T) {
	id := newIdentity(t)
	enc, err := Encrypt([]byte("Content-Type: text/plain\r\n\r\nhi\r\n"), []*x509.Certificate{id.cert})
	if err != nil {
		t.Fatal(err)
	}
	head, body, found := bytes.Cut(enc, []byte("\r\n\r\n"))
	if !found {
		t.Fatal("the encrypted message has no header/body separator")
	}
	der, err := decodeBase64Body(body)
	if err != nil {
		t.Fatal(err)
	}
	for x := 1; x < 256; x++ {
		bad := append([]byte{}, der...)
		bad[len(bad)-1] ^= byte(x)
		raw := append(append(append([]byte{}, head...), "\r\n\r\n"...), base64.StdEncoding.EncodeToString(bad)...)
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("last ciphertext byte xor %#x: Decrypt panics: %v", x, p)
				}
			}()
			if out, err := Decrypt(raw, id.cert, id.key); err == nil && bytes.Equal(out, []byte("Content-Type: text/plain\r\n\r\nhi\r\n")) {
				t.Errorf("xor %#x: altered ciphertext decrypted to the original", x)
			}
		}()
	}
}
