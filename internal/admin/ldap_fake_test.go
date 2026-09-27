package admin

import (
	"slices"

	"hermex/internal/directory"
)

// The fakeDir LDAP binding reads: bindings live in ldapBindings, connections in
// ldapConns, and a binding's effective configuration merges the two the way the
// directory does (the connection's transport, the binding's mapping and domain).

func (f *fakeDir) ListLDAPBindings(connectionID int64) ([]directory.LDAPBinding, error) {
	if err := f.readErrs["ListLDAPBindings"]; err != nil {
		return nil, err
	}
	var out []directory.LDAPBinding
	for _, b := range f.ldapBindings {
		if connectionID == 0 || b.ConnectionID == connectionID {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b directory.LDAPBinding) int { return int(a.ID - b.ID) })
	return out, nil
}

func (f *fakeDir) LDAPConfigForBinding(id int64) (directory.LDAPConfig, bool, error) {
	if err := f.readErrs["LDAPConfigForBinding"]; err != nil {
		return directory.LDAPConfig{}, false, err
	}
	b, ok := f.ldapBindings[id]
	if !ok {
		return directory.LDAPConfig{}, false, nil
	}
	c := f.ldapConns[b.ConnectionID]
	m := b.Mapping
	cfg := directory.LDAPConfig{
		URI: c.URI, StartTLS: c.StartTLS, BindDN: c.BindDN, BindPassword: c.BindPassword,
		BaseDN: c.BaseDN, UsernameAttr: c.UsernameAttr, SyncFields: m.Fields, AliasAttr: m.AliasAttr,
		SyncGroups: m.SyncGroups, GroupBaseDN: m.GroupBaseDN, GroupFilter: m.GroupFilter,
		SyncContacts: m.SyncContacts, ContactBaseDN: m.ContactBaseDN, ContactFilter: m.ContactFilter,
		DomainID: b.DomainID, Domain: b.Domain,
	}
	if m.BaseDN != "" {
		cfg.BaseDN = m.BaseDN
	}
	return cfg, true, nil
}
