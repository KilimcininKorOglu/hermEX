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
