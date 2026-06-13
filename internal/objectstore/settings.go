package objectstore

import "hermex/internal/mapi"

// GetWebmailSettings returns the webmail settings JSON stored on the store root,
// or the empty string when none has been saved yet. Settings live as a single
// MAPI property (PrWebmailSettings) rather than in a dedicated table, so they
// share the object store's storage and transaction model — the same "everything
// is a property" shape the rest of the store uses.
func (s *Store) GetWebmailSettings() (string, error) {
	props, err := s.GetStoreProperties(mapi.PrWebmailSettings)
	if err != nil {
		return "", err
	}
	if v, ok := props.Get(mapi.PrWebmailSettings); ok {
		if str, ok := v.(string); ok {
			return str, nil
		}
	}
	return "", nil
}

// SetWebmailSettings saves the webmail settings JSON as the store-root settings
// property, replacing any previous value.
func (s *Store) SetWebmailSettings(settingsJSON string) error {
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrWebmailSettings, Value: settingsJSON},
	})
}
