package webmail

import (
	"encoding/json"

	"hermex/internal/objectstore"
)

// settingsSchemaVersion is stamped into stored settings for cheap forward
// compatibility.
const settingsSchemaVersion = 1

// webmailSettings holds a user's webmail preferences. It is persisted as a
// single JSON blob in a store-root MAPI property (objectstore.GetWebmailSettings),
// so preferences live as a property rather than in a dedicated table. The schema
// is original to this project.
type webmailSettings struct {
	SchemaVersion         int         `json:"schemaVersion"`
	ComposeFormat         string      `json:"composeFormat"` // "html" | "plain"
	Signatures            []signature `json:"signatures"`
	DefaultSignatureNew   string      `json:"defaultSignatureNew"`   // signature id for new messages, or ""
	DefaultSignatureReply string      `json:"defaultSignatureReply"` // signature id for replies/forwards, or ""
}

// signature is one named signature. HTML holds the signature markup when IsHTML
// is true, or plain text otherwise.
type signature struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	HTML   string `json:"html"`
	IsHTML bool   `json:"isHTML"`
}

// defaultSettings is what a mailbox uses until it saves its own preferences.
func defaultSettings() webmailSettings {
	return webmailSettings{SchemaVersion: settingsSchemaVersion, ComposeFormat: "html"}
}

// loadSettings reads and decodes a mailbox's webmail settings, returning the
// defaults when none have been stored yet.
func loadSettings(st *objectstore.Store) (webmailSettings, error) {
	raw, err := st.GetWebmailSettings()
	if err != nil {
		return webmailSettings{}, err
	}
	if raw == "" {
		return defaultSettings(), nil
	}
	var s webmailSettings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return webmailSettings{}, err
	}
	if s.ComposeFormat == "" {
		s.ComposeFormat = "html"
	}
	return s, nil
}

// saveSettings encodes and stores a mailbox's webmail settings, stamping the
// current schema version.
func saveSettings(st *objectstore.Store, s webmailSettings) error {
	s.SchemaVersion = settingsSchemaVersion
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.SetWebmailSettings(string(b))
}
