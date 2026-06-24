package objectstore

import (
	"encoding/json"
	"strings"

	"hermex/internal/mapi"
)

// SmimeIdentity is a user's stored S/MIME identity: the password-protected
// PKCS#12 container and the public certificate (DER) extracted from it. The
// container stays encrypted at rest under its own passphrase, which the webmail
// unlocks per session and never persists. Both live as JSON on a single store
// property, mirroring how webmail settings are stored — no bespoke table.
type SmimeIdentity struct {
	// Mode is "server" (the encrypted P12 below holds the key, the server signs
	// and decrypts) or "browser" (only Cert is set; the private key lives in the
	// user's browser and the server never holds it).
	Mode string
	P12  []byte
	Cert []byte
}

// identityBlob is the JSON shape persisted in PrSmimeIdentity; json encodes
// []byte fields as base64.
type identityBlob struct {
	Mode string `json:"mode,omitempty"`
	P12  []byte `json:"p12"`
	Cert []byte `json:"cert"`
}

// SetSmimeIdentity stores the user's S/MIME identity, replacing any previous one.
func (s *Store) SetSmimeIdentity(id SmimeIdentity) error {
	blob, err := json.Marshal(identityBlob{Mode: id.Mode, P12: id.P12, Cert: id.Cert})
	if err != nil {
		return err
	}
	return s.SetStoreProperties(mapi.PropertyValues{{Tag: mapi.PrSmimeIdentity, Value: string(blob)}})
}

// GetSmimeIdentity returns the stored S/MIME identity; ok is false when none has
// been uploaded.
func (s *Store) GetSmimeIdentity() (id SmimeIdentity, ok bool, err error) {
	props, err := s.GetStoreProperties(mapi.PrSmimeIdentity)
	if err != nil {
		return SmimeIdentity{}, false, err
	}
	v, present := props.Get(mapi.PrSmimeIdentity)
	if !present {
		return SmimeIdentity{}, false, nil
	}
	str, _ := v.(string)
	if str == "" {
		return SmimeIdentity{}, false, nil
	}
	var b identityBlob
	if err := json.Unmarshal([]byte(str), &b); err != nil {
		return SmimeIdentity{}, false, err
	}
	mode := b.Mode
	if mode == "" { // infer for records written before Mode existed
		if len(b.P12) > 0 {
			mode = "server"
		} else {
			mode = "browser"
		}
	}
	return SmimeIdentity{Mode: mode, P12: b.P12, Cert: b.Cert}, true, nil
}

// ClearSmimeIdentity removes the stored S/MIME identity.
func (s *Store) ClearSmimeIdentity() error {
	return s.SetStoreProperties(mapi.PropertyValues{{Tag: mapi.PrSmimeIdentity, Value: ""}})
}

// recipientCerts loads the address→DER recipient certificate map (empty when none
// stored).
func (s *Store) recipientCerts() (map[string][]byte, error) {
	props, err := s.GetStoreProperties(mapi.PrSmimeCertStore)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	if v, ok := props.Get(mapi.PrSmimeCertStore); ok {
		if str, ok := v.(string); ok && str != "" {
			if err := json.Unmarshal([]byte(str), &out); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// saveRecipientCerts persists the recipient certificate map.
func (s *Store) saveRecipientCerts(m map[string][]byte) error {
	blob, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.SetStoreProperties(mapi.PropertyValues{{Tag: mapi.PrSmimeCertStore, Value: string(blob)}})
}

// PutRecipientCert stores (or replaces) a recipient's encryption certificate,
// keyed by lowercased address.
func (s *Store) PutRecipientCert(address string, certDER []byte) error {
	m, err := s.recipientCerts()
	if err != nil {
		return err
	}
	m[strings.ToLower(strings.TrimSpace(address))] = certDER
	return s.saveRecipientCerts(m)
}

// GetRecipientCert returns a recipient's stored certificate; ok is false when the
// address has none.
func (s *Store) GetRecipientCert(address string) (certDER []byte, ok bool, err error) {
	m, err := s.recipientCerts()
	if err != nil {
		return nil, false, err
	}
	der, ok := m[strings.ToLower(strings.TrimSpace(address))]
	return der, ok, nil
}

// ListRecipientCerts returns all stored recipient certificates by address.
func (s *Store) ListRecipientCerts() (map[string][]byte, error) {
	return s.recipientCerts()
}

// DeleteRecipientCert removes a recipient's stored certificate (a no-op when
// absent).
func (s *Store) DeleteRecipientCert(address string) error {
	m, err := s.recipientCerts()
	if err != nil {
		return err
	}
	delete(m, strings.ToLower(strings.TrimSpace(address)))
	return s.saveRecipientCerts(m)
}
