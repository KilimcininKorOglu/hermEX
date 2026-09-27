package admin

import (
	"slices"

	"hermex/internal/directory"
)

// The fakeDir LDAP connection and binding writes, over the ldapConns and
// ldapBindings maps. A domain binds once, as in the directory.

func (f *fakeDir) CreateLDAPConnection(c directory.LDAPConnection) (int64, error) {
	if f.ldapConns == nil {
		f.ldapConns = map[int64]directory.LDAPConnection{}
	}
	c.ID = int64(len(f.ldapConns) + 1)
	f.ldapConns[c.ID] = c
	return c.ID, nil
}

func (f *fakeDir) UpdateLDAPConnection(c directory.LDAPConnection) (bool, error) {
	if _, ok := f.ldapConns[c.ID]; !ok {
		return false, nil
	}
	f.ldapConns[c.ID] = c
	return true, nil
}

func (f *fakeDir) GetLDAPConnection(id int64) (directory.LDAPConnection, bool, error) {
	if err := f.readErrs["GetLDAPConnection"]; err != nil {
		return directory.LDAPConnection{}, false, err
	}
	c, ok := f.ldapConns[id]
	return c, ok, nil
}

func (f *fakeDir) ListLDAPConnections() ([]directory.LDAPConnection, error) {
	if err := f.readErrs["ListLDAPConnections"]; err != nil {
		return nil, err
	}
	out := make([]directory.LDAPConnection, 0, len(f.ldapConns))
	for _, c := range f.ldapConns {
		c.BindPassword = ""
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b directory.LDAPConnection) int { return int(a.ID - b.ID) })
	return out, nil
}

func (f *fakeDir) DeleteLDAPConnection(id int64) (bool, error) {
	if _, ok := f.ldapConns[id]; !ok {
		return false, nil
	}
	delete(f.ldapConns, id)
	for bid, b := range f.ldapBindings {
		if b.ConnectionID == id {
			delete(f.ldapBindings, bid)
		}
	}
	return true, nil
}

func (f *fakeDir) CreateLDAPBinding(b directory.LDAPBinding) (int64, error) {
	if f.ldapBindings == nil {
		f.ldapBindings = map[int64]directory.LDAPBinding{}
	}
	for _, have := range f.ldapBindings {
		if have.DomainID == b.DomainID {
			return 0, directory.ErrLDAPDomainBound
		}
	}
	b.ID = int64(len(f.ldapBindings) + 1)
	f.ldapBindings[b.ID] = b
	return b.ID, nil
}

func (f *fakeDir) UpdateLDAPBinding(b directory.LDAPBinding) (bool, error) {
	have, ok := f.ldapBindings[b.ID]
	if !ok {
		return false, nil
	}
	have.ConnectionID, have.Preset, have.Mapping = b.ConnectionID, b.Preset, b.Mapping
	f.ldapBindings[b.ID] = have
	return true, nil
}

func (f *fakeDir) GetLDAPBinding(id int64) (directory.LDAPBinding, bool, error) {
	if err := f.readErrs["GetLDAPBinding"]; err != nil {
		return directory.LDAPBinding{}, false, err
	}
	b, ok := f.ldapBindings[id]
	return b, ok, nil
}

func (f *fakeDir) DeleteLDAPBinding(id int64) (bool, error) {
	if _, ok := f.ldapBindings[id]; !ok {
		return false, nil
	}
	delete(f.ldapBindings, id)
	return true, nil
}
