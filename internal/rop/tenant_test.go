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
	resp, _ := outsider.Dispatch(delegateLogonRequest(0, 0x01, userDNFor("boss@hermex.test")), []uint32{0xFFFFFFFF})
	if ec, _ := readLogonResponseFlags(t, resp); ec != ecAccessDenied {
		t.Errorf("logon from another organization ec = %#x, want access denied", ec)
	}

	peer := NewSession(peerDir, accounts, "peer@hermex.test")
	defer peer.Close()
	resp, _ = peer.Dispatch(delegateLogonRequest(0, 0x01, userDNFor("boss@hermex.test")), []uint32{0xFFFFFFFF})
	if ec, _ := readLogonResponseFlags(t, resp); ec != ecSuccess {
		t.Errorf("logon within the organization ec = %#x, want success", ec)
	}
}
