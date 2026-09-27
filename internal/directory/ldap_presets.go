package directory

// Mapping presets name a starting downsync mapping for a directory schema, so an
// operator binding a domain picks the schema instead of typing every attribute. A
// binding records the preset it started from, and resetting it restores exactly
// this mapping. Group and contact sync stay off in every preset: turning them on
// is a deliberate choice per domain.
const (
	LDAPPresetAD            = "ad"            // Microsoft Active Directory
	LDAPPresetInetOrgPerson = "inetorgperson" // RFC 2798 inetOrgPerson (OpenLDAP and others)
)

// ldapPresetNames lists the presets in display order.
var ldapPresetNames = []string{LDAPPresetAD, LDAPPresetInetOrgPerson}

// LDAPPresetNames returns the known preset names in display order.
func LDAPPresetNames() []string {
	return append([]string(nil), ldapPresetNames...)
}

// LDAPPreset returns a fresh copy of a preset's mapping, ok=false for an unknown
// name. Every enabled field reads its standard attribute unless the preset names
// another one.
func LDAPPreset(name string) (LDAPMapping, bool) {
	switch name {
	case LDAPPresetAD:
		return LDAPMapping{
			Fields: enabledFields(map[string]string{
				"displayName": "", "givenName": "", "surname": "", "title": "", "department": "",
				"company": "", "office": "", "businessPhone": "", "mobile": "",
			}),
			AliasAttr: "proxyAddresses",
		}, true
	case LDAPPresetInetOrgPerson:
		return LDAPMapping{
			Fields: enabledFields(map[string]string{
				"displayName": "", "givenName": "", "surname": "", "title": "",
				"department": "departmentNumber", "company": "o", "office": "roomNumber",
				"businessPhone": "", "mobile": "",
			}),
		}, true
	}
	return LDAPMapping{}, false
}

// enabledFields turns a field-key to attribute-override map into enabled sync
// fields (an empty override reads the field's standard attribute).
func enabledFields(attrs map[string]string) map[string]LDAPSyncField {
	out := make(map[string]LDAPSyncField, len(attrs))
	for k, a := range attrs {
		out[k] = LDAPSyncField{Enabled: true, Attr: a}
	}
	return out
}
