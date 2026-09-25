package webmail2api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"hermex/internal/logging"
)

// The S/MIME vault is a browser-mode identity the browser sealed under the user's
// password (PBKDF2-SHA256 into an AES-256-GCM key). The server stores the sealed
// envelope with the mailbox and returns it to its owner, so the identity works in
// every browser the owner signs in to, while the server never holds anything it
// can open.

// Vault limits. The work factor floor is the one the browser seals with, so a
// weaker envelope from a tampered client is refused; the ceiling keeps a stored
// envelope from making every later unlock hang. 64 KiB holds a PKCS#8 key and a
// certificate chain with room to spare.
const (
	vaultMinIterations = 600_000
	vaultMaxIterations = 10_000_000
	vaultMaxCiphertext = 64 << 10
)

// errBadVault reports an envelope that is not a sealed identity this server stores.
var errBadVault = errors.New("webmail2api: invalid S/MIME key vault")

// vaultEnvelope is the sealed identity as the browser writes it.
type vaultEnvelope struct {
	V    int    `json:"v"`
	KDF  string `json:"kdf"`
	Iter int    `json:"iter"`
	Salt string `json:"salt"`
	IV   string `json:"iv"`
	CT   string `json:"ct"`
}

// validVault checks an envelope's shape and returns it re-encoded, so what is
// stored is exactly the fields this format has. The ciphertext is not, and cannot
// be, checked beyond its size: only the owner's password opens it.
func validVault(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var env vaultEnvelope
	if err := dec.Decode(&env); err != nil {
		return nil, errBadVault
	}
	if env.V != 1 || env.KDF != "PBKDF2-SHA256" || env.Iter < vaultMinIterations || env.Iter > vaultMaxIterations {
		return nil, errBadVault
	}
	if decodedLen(env.Salt) != 16 || decodedLen(env.IV) != 12 {
		return nil, errBadVault
	}
	if n := decodedLen(env.CT); n <= 0 || n > vaultMaxCiphertext {
		return nil, errBadVault
	}
	return json.Marshal(env)
}

// decodedLen is the length of a standard base64 value, -1 when it does not decode.
func decodedLen(s string) int {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return -1
	}
	return len(b)
}

// handleGetSmimeVault returns the caller's sealed identity, 404 when they keep
// none on the server. The envelope is theirs alone and is never cached.
func (s *Server) handleGetSmimeVault(w http.ResponseWriter, r *http.Request) {
	st, c, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	w.Header().Set("Cache-Control", "no-store")
	id, found, err := st.GetSmimeIdentity()
	if err != nil {
		logError("smime-vault", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	if !found || len(id.Vault) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no stored key"})
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(id.Vault))
}
