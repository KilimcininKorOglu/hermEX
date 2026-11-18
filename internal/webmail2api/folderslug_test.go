package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// twinFolders is a mailbox holding two user folders named "2024", one under
// "Projects" and one under "Archive" (names are unique only among siblings,
// [MS-OXCFOLD] 3.2.5.2), each with one message of its own.
type twinFolders struct {
	dir                    string
	projects, a2024, b2024 int64
	call                   func(method, target, body string) *httptest.ResponseRecorder
}

func newTwinFolders(t *testing.T) twinFolders {
	t.Helper()
	tf := twinFolders{dir: t.TempDir()}
	st, err := objectstore.Open(tf.dir)
	mustNoErr(t, "open the store", err)
	tf.projects, err = st.CreateFolder(nil, "Projects")
	mustNoErr(t, "create Projects", err)
	other, err := st.CreateFolder(nil, "Old")
	mustNoErr(t, "create Old", err)
	tf.a2024, err = st.CreateFolder(&tf.projects, "2024")
	mustNoErr(t, "create Projects/2024", err)
	tf.b2024, err = st.CreateFolder(&other, "2024")
	mustNoErr(t, "create Old/2024", err)
	for fid, subj := range map[int64]string{tf.a2024: "in-projects", tf.b2024: "in-old"} {
		raw := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: " + subj +
			"\r\nDate: Mon, 01 Jun 2026 10:00:00 +0000\r\n\r\nbody\r\n")
		_, err := st.AppendMessage(fid, raw, time.Now(), 0)
		mustNoErr(t, "append "+subj, err)
	}
	st.Close()

	secret := []byte("folder-slug-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	tf.call = func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: tf.dir, Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	return tf
}

// subjects lists the subjects GET /api/v1/mail/{folder} answers.
func (tf twinFolders) subjects(t *testing.T, folder string) []string {
	t.Helper()
	rec := tf.call(http.MethodGet, "/api/v1/mail/"+folder, "")
	var out struct {
		Emails []struct {
			Subject string `json:"subject"`
		} `json:"emails"`
	}
	mustNoErr(t, "decode the listing of "+folder, json.Unmarshal(rec.Body.Bytes(), &out))
	var subj []string
	for _, e := range out.Emails {
		subj = append(subj, e.Subject)
	}
	return subj
}

// folderName reads a folder's display name from the store.
func (tf twinFolders) folderName(t *testing.T, fid int64) string {
	t.Helper()
	st, err := objectstore.Open(tf.dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	props, err := st.GetFolderProperties(fid, mapi.PrDisplayName)
	mustNoErr(t, "read the folder name", err)
	return propStr(props, mapi.PrDisplayName)
}

// TestMailboxesTellsSameNamedFoldersApart proves the folder listing gives each
// user folder its own id and its parent, so two folders named "2024" are two
// distinct entries that the sidebar can place under their parents.
func TestMailboxesTellsSameNamedFoldersApart(t *testing.T) {
	tf := newTwinFolders(t)
	rec := tf.call(http.MethodGet, "/api/v1/mailboxes", "")
	var out struct {
		Folders []userFolderJSON `json:"folders"`
	}
	mustNoErr(t, "decode /mailboxes", json.Unmarshal(rec.Body.Bytes(), &out))
	got := map[string]userFolderJSON{}
	for _, f := range out.Folders {
		got[f.ID] = f
	}
	a, b := got["f"+strconv.FormatInt(tf.a2024, 10)], got["f"+strconv.FormatInt(tf.b2024, 10)]
	wantEq(t, "name of Projects/2024", a.Name, "2024")
	wantEq(t, "name of Old/2024", b.Name, "2024")
	wantEq(t, "parent of Projects/2024", a.Parent, "f"+strconv.FormatInt(tf.projects, 10))
	if a.Parent == b.Parent {
		t.Errorf("both folders named 2024 report the parent %q", a.Parent)
	}
}

// TestASlugReachesOnlyItsOwnFolder proves each of two same-named folders is
// listed, renamed and emptied by its own slug, with no effect on its twin.
func TestASlugReachesOnlyItsOwnFolder(t *testing.T) {
	tf := newTwinFolders(t)
	a, b := customSlug(tf.a2024), customSlug(tf.b2024)
	wantEq(t, "listing of the second 2024", strings.Join(tf.subjects(t, b), ","), "in-old")
	wantEq(t, "listing of the first 2024", strings.Join(tf.subjects(t, a), ","), "in-projects")

	rec := tf.call(http.MethodPut, "/api/v1/folders/"+b, `{"name":"2024-old"}`)
	wantEq(t, "rename status", rec.Code, http.StatusOK)
	wantEq(t, "the renamed folder", tf.folderName(t, tf.b2024), "2024-old")
	wantEq(t, "its twin", tf.folderName(t, tf.a2024), "2024")

	rec = tf.call(http.MethodPost, "/api/v1/folders/"+b+"/empty", "")
	wantEq(t, "empty status", rec.Code, http.StatusOK)
	wantEq(t, "messages left in the emptied folder", len(tf.subjects(t, b)), 0)
	wantEq(t, "listing of the twin after the empty", strings.Join(tf.subjects(t, a), ","), "in-projects")
}

// TestASlugNamesOnlyAUserFolder proves a slug resolves only to a listed user
// folder: a built-in folder's id ("f13" is the Inbox), ids that are zero,
// negative, zero-padded, overflowing or missing, and text that is not a slug all
// answer as an unknown folder, so a guessed id reaches nothing outside the list.
func TestASlugNamesOnlyAUserFolder(t *testing.T) {
	tf := newTwinFolders(t)
	st, err := objectstore.Open(tf.dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	padded := "f0" + strconv.FormatInt(tf.a2024, 10)
	for _, slug := range []string{"f" + strconv.Itoa(int(mapi.PrivateFIDInbox)), "f0", "f-1", padded,
		"f99999999999999999999", "f999999", "f", "F" + strconv.FormatInt(tf.a2024, 10), "f1x"} {
		if fid, ok := userFolderBySlug(st, slug); ok {
			t.Errorf("slug %q resolved to folder %d", slug, fid)
		}
	}
	if fid, ok := userFolderBySlug(st, customSlug(tf.a2024)); !ok || fid != tf.a2024 {
		t.Errorf("the slug of Projects/2024 resolved to %d, %v", fid, ok)
	}
}

// TestAListingAnswersWithTheSlug proves a folder listed by its legacy display
// name answers with the folder's slug in every message id and folder field, so
// the client holds one address per folder whatever it sent.
func TestAListingAnswersWithTheSlug(t *testing.T) {
	tf := newTwinFolders(t)
	st, err := objectstore.Open(tf.dir)
	mustNoErr(t, "open the store", err)
	raw := []byte("From: bob@hermex.test\r\nSubject: top\r\nDate: Mon, 01 Jun 2026 10:00:00 +0000\r\n\r\nbody\r\n")
	_, err = st.AppendMessage(tf.projects, raw, time.Now(), 0)
	mustNoErr(t, "append to Projects", err)
	st.Close()

	rec := tf.call(http.MethodGet, "/api/v1/mail/Projects", "")
	var out struct {
		Emails []struct {
			ID     string `json:"id"`
			Folder string `json:"folder"`
		} `json:"emails"`
	}
	mustNoErr(t, "decode the listing", json.Unmarshal(rec.Body.Bytes(), &out))
	if len(out.Emails) != 1 {
		t.Fatalf("listing holds %d messages, want 1", len(out.Emails))
	}
	slug := customSlug(tf.projects)
	wantEq(t, "folder field", out.Emails[0].Folder, slug)
	if !strings.HasPrefix(out.Emails[0].ID, slug+":") {
		t.Errorf("message id %q does not start with %q", out.Emails[0].ID, slug+":")
	}
}

// TestAFolderNamedLikeASlugCannotTakeIt proves a folder whose display name is
// another folder's slug does not capture that slug: the slug names the folder of
// that id.
func TestAFolderNamedLikeASlugCannotTakeIt(t *testing.T) {
	tf := newTwinFolders(t)
	st, err := objectstore.Open(tf.dir)
	mustNoErr(t, "open the store", err)
	_, err = st.CreateFolder(nil, customSlug(tf.b2024))
	mustNoErr(t, "create the impostor", err)
	st.Close()
	wantEq(t, "listing of the slug", strings.Join(tf.subjects(t, customSlug(tf.b2024)), ","), "in-old")
}
