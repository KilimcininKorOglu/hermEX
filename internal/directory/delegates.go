package directory

import "hermex/internal/objectstore"

// Delegates returns the public-delegate list stored in the mailbox of userAddr —
// the addresses permitted to act for it — for the address book's public-delegates
// container. It resolves the address to its mailbox store and reads the list. An
// address with no local mailbox (or no delegates set) yields none. The list lives
// with the mailbox, not in the directory database.
func (d *SQLDirectory) Delegates(userAddr string) ([]string, error) {
	maildir, ok := d.Resolve(userAddr)
	if !ok {
		return nil, nil
	}
	st, err := objectstore.Open(maildir)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return st.GetDelegates()
}

// SetDelegates replaces the public-delegate list of the mailbox at userAddr. An
// address with no local mailbox is a no-op (there is no store to write).
func (d *SQLDirectory) SetDelegates(userAddr string, list []string) error {
	maildir, ok := d.Resolve(userAddr)
	if !ok {
		return nil
	}
	st, err := objectstore.Open(maildir)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.SetDelegates(list)
}
