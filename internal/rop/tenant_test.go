package rop

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
)

// TestLogonRefusesAMailboxOfAnotherOrganization proves a folder grant does not
// open a mailbox across organizations: the target granted the caller Reviewer on
// its Inbox by name, yet the caller, in another domain, is refused at logon,
// while the same grant within the domain opens it.
func TestLogonRefusesAMailboxOfAnotherOrganization(t *testing.T) {
	callerDir, targetDir, peerDir := provisionedDir(t), provisionedDir(t), provisionedDir(t)
	grantFolderPermission(t, targetDir, int64(mapi.PrivateFIDInbox), "delegate@other.test", mapi.RightsReviewer)
	grantFolderPermission(t, targetDir, int64(mapi.PrivateFIDInbox), "peer@hermex.test", mapi.RightsReviewer)
	accounts := directory.StaticAccounts{
		"boss@hermex.test":    {MailboxPath: targetDir},
		"delegate@other.test": {MailboxPath: callerDir},
		"peer@hermex.test":    {MailboxPath: peerDir},
	}

	outsider := NewSession(callerDir, accounts, "delegate@other.test")
	defer outsider.Close()
	resp, handles := outsider.Dispatch(delegateLogonRequest(0, 0x01, userDNFor("boss@hermex.test")), []uint32{0xFFFFFFFF})
	if ec, flags := readLogonResponseFlags(t, resp); ec != ecAccessDenied || flags != 0 {
		t.Errorf("logon from another organization = (ec=%#x, flags=%#x), want (ecAccessDenied, 0)", ec, flags)
	}
	// The refusal opens nothing: no logon handle the outsider could address.
	if len(handles) != 1 || handles[0] != 0xFFFFFFFF {
		t.Errorf("refused logon handles = %#x, want the unset handle", handles)
	}

	peer := NewSession(peerDir, accounts, "peer@hermex.test")
	defer peer.Close()
	resp, peerHandles := peer.Dispatch(delegateLogonRequest(0, 0x01, userDNFor("boss@hermex.test")), []uint32{0xFFFFFFFF})
	if ec, flags := readLogonResponseFlags(t, resp); ec != ecSuccess || flags != responseFlagReserved {
		t.Errorf("logon within the organization = (ec=%#x, flags=%#x), want (ecSuccess, %#x)", ec, flags, responseFlagReserved)
	}
	if len(peerHandles) != 1 || peerHandles[0] == 0xFFFFFFFF {
		t.Errorf("peer logon handles = %#x, want an allocated handle", peerHandles)
	}
}
