package smime

import (
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
