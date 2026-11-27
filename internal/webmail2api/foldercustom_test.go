package webmail2api

import (
	"net/http"
	"testing"
	"time"
)

// customFolderMailbox files one message in a user folder "Projects" and
// returns the mailbox and the folder id.
func customFolderMailbox(t *testing.T) (string, int64) {
	t.Helper()
	dir := freshMailbox(t)
	st := openMailbox(t, dir)
	cfid, err := st.CreateFolder(nil, "Projects")
	mustNoErr(t, "create folder", err)
	_, err = st.AppendMessage(cfid, []byte("From: a@b.test\r\nSubject: filed away\r\n\r\nhi\r\n"), time.Now(), 0)
	mustNoErr(t, "append", err)
	return dir, cfid
}

type folderListing struct {
	Emails []struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
	} `json:"emails"`
}

// TestCustomFolderRoundTrip proves a message filed into a custom (non-built-in)
// folder is visible in that folder's list view AND can be opened by its id.
// Both paths used to resolve the folder via the well-known-slug table only, so a
// custom folder like "Projects" listed empty and its messages returned 404 on
// open. resolveFolder now resolves a user folder by its id slug, closing that
// gap for the list/open/action handlers alike.
func TestCustomFolderRoundTrip(t *testing.T) {
	dir, cfid := customFolderMailbox(t)
	do := apiHarnessFor(t, dir)

	list := okBody[folderListing](t, "list", do(http.MethodGet, "/api/v1/mail/"+customSlug(cfid), ""))
	if len(list.Emails) != 1 {
		t.Fatalf("custom folder list returned %d messages, want 1", len(list.Emails))
	}
	wantEq(t, "listed subject", list.Emails[0].Subject, "filed away")
	wantStatus(t, "open "+list.Emails[0].ID, do(http.MethodGet, "/api/v1/mail/message?id="+list.Emails[0].ID, ""), http.StatusOK)
}

// TestACustomFolderIsReachedOnlyByItsSlug: a user folder is addressed by its
// f<id> slug alone. Its display name, an unknown id and a malformed slug list
// nothing of it, and a message id under such a folder does not open.
func TestACustomFolderIsReachedOnlyByItsSlug(t *testing.T) {
	dir, cfid := customFolderMailbox(t)
	do := apiHarnessFor(t, dir)
	for _, ref := range []string{"Projects", "f999999", customSlug(cfid) + "x"} {
		rec := do(http.MethodGet, "/api/v1/mail/"+ref, "")
		if rec.Code != http.StatusOK {
			continue
		}
		if l := decodeBody[folderListing](t, ref, rec); len(l.Emails) != 0 {
			t.Errorf("folder %q listed %d messages of Projects", ref, len(l.Emails))
		}
	}
	for _, id := range []string{"Projects:1", "f999999:1"} {
		if rec := do(http.MethodGet, "/api/v1/mail/message?id="+id, ""); rec.Code == http.StatusOK {
			t.Errorf("open %s answered 200", id)
		}
	}
}
