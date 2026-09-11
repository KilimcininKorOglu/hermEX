package ews

import (
	"strings"
	"testing"
)

// TestGetFolderResolvesTheDesktopFolders covers the folders a desktop client
// asks for by name on connect. Answering "no such folder" for the recipient
// cache makes the client create one of its own, and the two never agree on
// which addresses it has autocompleted.
func TestGetFolderResolvesTheDesktopFolders(t *testing.T) {
	ts, _ := seededEWS(t)
	for _, c := range []struct{ id, name string }{
		{"archive", "Archive"},
		{"conversationhistory", "Conversation History"},
		{"recipientcache", "Recipient Cache"},
		// The rest of the built-in hierarchy. Every mailbox holds these, so a client
		// that resolves the distinguished set must find each one.
		{"searchfolders", "Finder"},
		{"syncissues", "Sync Issues"},
		{"conflicts", "Conflicts"},
		{"localfailures", "Local Failures"},
		{"serverfailures", "Server Failures"},
		{"quickcontacts", "Quick Contacts"},
		{"imcontactlist", "IM Contacts List"},
	} {
		resp, out := soapPost(t, ts, distinguishedGetFolder(c.id), true)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status = %d: %s", c.id, resp.StatusCode, out)
		}
		if !strings.Contains(out, `ResponseClass="Success"`) {
			t.Errorf("%s was not resolved: %s", c.id, out)
			continue
		}
		if !strings.Contains(out, c.name) {
			t.Errorf("%s did not carry the display name %q: %s", c.id, c.name, out)
		}
	}
}

// TestGetFolderRefusesAFolderTheMailboxLacks is the negative control: a name is
// resolved because the mailbox holds that folder, not because the name is known.
// hermEX has no archive mailbox and no voice mail, so those names stay
// ErrorFolderNotFound, which is what lets a client probing the whole distinguished
// set skip them instead of aborting the batch.
func TestGetFolderRefusesAFolderTheMailboxLacks(t *testing.T) {
	ts, _ := seededEWS(t)
	for _, id := range []string{"archivemsgfolderroot", "voicemail", "favorites"} {
		_, out := soapPost(t, ts, distinguishedGetFolder(id), true)
		if !strings.Contains(out, "ErrorFolderNotFound") {
			t.Errorf("%s was resolved, but the mailbox holds no such folder: %s", id, out)
		}
	}
}
