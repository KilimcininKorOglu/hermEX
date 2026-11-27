package webmail2api

import (
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// A user folder is addressed by "f" and its folder id in decimal ("f300"), never
// by its display name: names are unique only among siblings ([MS-OXCFOLD]
// 3.2.5.2), so two folders of one name under different parents would otherwise
// be one address, and every operation on the second would reach the first. The
// built-in folders keep their fixed slugs (folderFID).

// customSlugPrefix starts the slug of a user folder.
const customSlugPrefix = "f"

// customSlug is the slug of the user folder fid.
func customSlug(fid int64) string {
	return customSlugPrefix + strconv.FormatInt(fid, 10)
}

// builtinSlugs are the fixed slugs of the built-in folders, the names folderFID
// resolves; the first slug of a folder is the one the server emits.
var builtinSlugs = map[int64]string{
	mapi.PrivateFIDInbox:        "inbox",
	mapi.PrivateFIDSentItems:    "sent",
	mapi.PrivateFIDDraft:        "drafts",
	mapi.PrivateFIDDeletedItems: "trash",
	mapi.PrivateFIDJunk:         "spam",
	mapi.PrivateFIDArchive:      "archive",
}

// slugOf is the one slug the server emits for a folder it resolved: the fixed
// slug of a built-in folder, else the id slug. Every id and folder field a
// response carries is built from the resolved folder through this, never echoed
// from the request, so a folder has one address whatever the client sent.
func slugOf(fid int64) string {
	if s, ok := builtinSlugs[fid]; ok {
		return s
	}
	return customSlug(fid)
}

// parseCustomSlug reads the folder id out of a user-folder slug: "f" followed by
// a positive decimal id with no sign and no leading zero, so each folder has
// exactly one spelling.
func parseCustomSlug(slug string) (int64, bool) {
	digits, ok := strings.CutPrefix(slug, customSlugPrefix)
	if !ok || digits == "" || digits[0] < '1' || digits[0] > '9' {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	fid, err := strconv.ParseInt(digits, 10, 64)
	return fid, err == nil
}

// userFolders lists the folders the SPA shows as the user's own: the folders of
// the mailbox's folder tree that a user created, the set /mailboxes returns.
func userFolders(st *objectstore.Store) ([]objectstore.FolderInfo, error) {
	folders, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	out := make([]objectstore.FolderInfo, 0, len(folders))
	for _, f := range folders {
		if isUserFolderID(f.ID) {
			out = append(out, f)
		}
	}
	return out, nil
}

// isUserFolderID reports whether fid is in the range handed out to user folders.
func isUserFolderID(fid int64) bool {
	return fid >= int64(mapi.PrivateFIDUnassignedStart)
}

// userFolderByRef resolves a user folder from the SPA by its slug only. A
// display name is not an address: two folders under different parents may share
// one.
func userFolderByRef(st *objectstore.Store, ref string) (int64, bool) {
	return userFolderBySlug(st, ref)
}

// userFolderBySlug resolves a user-folder slug to its folder id. It resolves
// exactly the folders userFolders lists, so a built-in folder ("f13" is the
// Inbox) or a folder outside the folder tree is not reachable this way.
func userFolderBySlug(st *objectstore.Store, slug string) (int64, bool) {
	fid, ok := parseCustomSlug(slug)
	if !ok || !isUserFolderID(fid) {
		return 0, false
	}
	folders, err := userFolders(st)
	if err != nil {
		st.LogSwallowedError("folder-slug.list", err)
		return 0, false
	}
	for _, f := range folders {
		if f.ID == fid {
			return fid, true
		}
	}
	return 0, false
}
