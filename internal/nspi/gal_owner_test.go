package nspi

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
)

// TestGalUserPropsListOwner proves the address book advertises a distribution list's
// owner as PR_EMS_AB_OWNER (the owner's EntryID = the Exchange managedBy attribute),
// and that an ownerless list or a mailbox user carries no such property.
func TestGalUserPropsListOwner(t *testing.T) {
	list := galUser{smtp: "crew@hermex.test", display: "Crew", dt: dtDistlist, owner: "alice@hermex.test"}
	v, ok := galUserProps(list).Get(mapi.PrEmsAbOwner)
	if !ok {
		t.Fatal("a list with an owner is missing PR_EMS_AB_OWNER")
	}
	want := permanentEntryID(dtMailuser, userDN("alice@hermex.test"))
	if got, isBytes := v.([]byte); !isBytes || !bytes.Equal(got, want) {
		t.Errorf("PR_EMS_AB_OWNER = %#v, want the owner's permanent EntryID", v)
	}

	if _, ok := galUserProps(galUser{smtp: "open@hermex.test", dt: dtDistlist}).Get(mapi.PrEmsAbOwner); ok {
		t.Error("an ownerless list must not carry PR_EMS_AB_OWNER")
	}
	if _, ok := galUserProps(galUser{smtp: "alice@hermex.test", dt: dtMailuser, owner: "x@y.test"}).Get(mapi.PrEmsAbOwner); ok {
		t.Error("a mailbox user must not carry PR_EMS_AB_OWNER")
	}
}
