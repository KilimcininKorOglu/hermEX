package rop

import (
	"slices"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/nspi"
	"hermex/internal/objectstore"
)

// logonIdentity is who a logon opened a store as: the store's owner and the user
// who logged on, the same address for an owner logon.
type logonIdentity struct {
	owner, user string
	ownerName   func() string
}

// computedStoreTags are the store properties the store computes; a read that asks
// for none of them skips the computation.
var computedStoreTags = []mapi.PropTag{
	mapi.PrContentCount, mapi.PrMessageSizeExtended, mapi.PrMessageSize, mapi.PrStoreState,
}

// quotaTags are the store quotas held in kibibytes with 0 meaning unlimited.
// [MS-OXCSTOR] 2.2.2.1.1.3 and 2.2.2.1.1.4 express no limit as an unset
// property, so a stored 0 is not served: a client would read it as a zero quota
// and call every mailbox full.
var quotaTags = []mapi.PropTag{mapi.PrProhibitReceiveQuota, mapi.PrProhibitSendQuota}

// storeProps returns the logon's store properties: the stored ones, overlaid with
// the ones the store computes and the logon's identity ([MS-OXCSTOR] 2.2.2.1.1),
// narrowed to tags, or all of them when tags is empty.
func storeProps(store *objectstore.Store, tags []mapi.PropTag, id *logonIdentity) (mapi.PropertyValues, error) {
	props, err := store.GetStoreProperties(tags...)
	if err != nil {
		return nil, err
	}
	for _, t := range quotaTags {
		if v, ok := props.Get(t); ok && v == int32(0) {
			props = removeTag(props, t)
		}
	}
	if requestsAny(tags, computedStoreTags) {
		computed, err := store.StoreComputedProps()
		if err != nil {
			return nil, err
		}
		for _, tv := range computed {
			if len(tags) == 0 || slices.Contains(tags, tv.Tag) {
				props.Set(tv.Tag, tv.Value)
			}
		}
	}
	addLogonIdentity(&props, tags, id)
	return props, nil
}

// addLogonIdentity sets the owner's and the logged-on user's address-book entry
// ids and the owner's display name, when the logon knows them.
func addLogonIdentity(props *mapi.PropertyValues, tags []mapi.PropTag, id *logonIdentity) {
	if id == nil || id.owner == "" {
		return
	}
	want := func(t mapi.PropTag) bool { return len(tags) == 0 || slices.Contains(tags, t) }
	if want(mapi.PrMailboxOwnerEntryID) {
		props.Set(mapi.PrMailboxOwnerEntryID, nspi.MailUserEntryID(id.owner))
	}
	if want(mapi.PrUserEntryID) && id.user != "" {
		props.Set(mapi.PrUserEntryID, nspi.MailUserEntryID(id.user))
	}
	if want(mapi.PrMailboxOwnerName) && id.ownerName != nil {
		if name := id.ownerName(); name != "" {
			props.Set(mapi.PrMailboxOwnerName, name)
		}
	}
}

// logonIdentityFor records who a logon opened a store as. owner is the delegated
// mailbox's address for a delegate logon and the caller's own otherwise.
func (s *Session) logonIdentityFor(owner string) *logonIdentity {
	if owner == "" {
		owner = s.owner
	}
	return &logonIdentity{owner: owner, user: s.owner, ownerName: func() string { return s.displayName(owner) }}
}

// displayName looks an address up in the directory's address book, as seen by
// the logged-on user, and returns its display name, or "" when the directory
// offers no address book or has no such entry.
func (s *Session) displayName(addr string) string {
	gal, ok := s.accounts.(directory.GAL)
	if !ok || addr == "" {
		return ""
	}
	entries, err := gal.SearchGAL(s.owner, addr, 10)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.EqualFold(e.Address, addr) {
			return e.DisplayName
		}
	}
	return ""
}
