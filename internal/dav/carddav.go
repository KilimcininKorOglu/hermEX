package dav

import (
	"io"
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxvcard"
)

// defaultMaxVCard caps a PUT body; a vCard, even with an embedded photo, is far
// smaller. It is the fallback when no operator limit is set.
const defaultMaxVCard = 4 << 20

// davResourceName stores the client-chosen object URL segment (e.g. "abc.vcf")
// as a string property on the contact, so a URL resolves back to its stored
// object and the href stays stable across edits. It is a neutral name in the
// public-strings namespace, distinct from the vCard UID.
var davResourceName = mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsPublicStrings, Name: "DavResourceName"}

// vcardResolver adapts the store's named-property allocator to oxvcard.
func vcardOptions(st *objectstore.Store) oxvcard.Options {
	return oxvcard.Options{Resolver: st.GetNamedPropIDs}
}

// resourceNameTag resolves the DAV resource-name property to its store proptag.
func resourceNameTag(st *objectstore.Store, create bool) (mapi.PropTag, bool, error) {
	ids, err := st.GetNamedPropIDs(create, []mapi.PropertyName{davResourceName})
	if err != nil {
		return 0, false, err
	}
	if ids[0] == 0 {
		return 0, false, nil
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtUnicode)), true, nil
}

// objectName returns an object's DAV resource name: the stored resource-name
// property if present, else a stable fallback derived from its EID with the
// collection's extension (".vcf" for contacts, ".ics" for calendar events).
func objectName(st *objectstore.Store, id int64, ext string) string {
	if tag, ok, err := resourceNameTag(st, false); err == nil && ok {
		if props, err := st.GetMessageProperties(id); err == nil {
			if v, ok := props.Get(tag); ok {
				if s, _ := v.(string); s != "" {
					return s
				}
			}
		}
	}
	return strconv.FormatInt(id, 10) + ext
}

// findObjectByName resolves a URL segment to its stored object within the given
// folder. The scan is O(folder size) per request, acceptable for typical
// collections; a property index is a later optimization.
func findObjectByName(st *objectstore.Store, folderID int64, ext, name string) (objectstore.FolderObject, bool, error) {
	objs, err := st.ListFolderObjects(folderID)
	if err != nil {
		return objectstore.FolderObject{}, false, err
	}
	for _, o := range objs {
		if objectName(st, o.ID, ext) == name {
			return o, true, nil
		}
	}
	return objectstore.FolderObject{}, false, nil
}

// handleGet serves a contact as a vCard. HEAD returns the same headers with no
// body.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, mailbox string) {
	kind, _, name := classify(r.URL.Path)
	if kind != kindObject {
		http.Error(w, "not a contact resource", http.StatusMethodNotAllowed)
		return
	}
	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	obj, found, err := findObjectByName(st, mapi.PrivateFIDContacts, ".vcf", name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	msg, err := st.OpenMessage(obj.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	vcf, err := oxvcard.Export(msg, vcardOptions(st))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("ETag", etag(obj.ChangeNumber))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Write(vcf)
}

// handlePut creates or replaces a contact from a vCard body. It honors
// If-None-Match: * (create-only) and If-Match (replace-guard), responding 201 on
// create and 204 on replace with the new ETag.
func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, mailbox string) {
	kind, _, name := classify(r.URL.Path)
	if kind != kindObject {
		http.Error(w, "not a contact resource", http.StatusMethodNotAllowed)
		return
	}
	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	existing, found, err := findObjectByName(st, mapi.PrivateFIDContacts, ".vcf", name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("If-None-Match") == "*" && found {
		http.Error(w, "already exists", http.StatusPreconditionFailed)
		return
	}
	if im := r.Header.Get("If-Match"); im != "" {
		if !found || im != etag(existing.ChangeNumber) {
			http.Error(w, "etag mismatch", http.StatusPreconditionFailed)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, s.vcardLimit()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	msg, err := oxvcard.Import(body, vcardOptions(st))
	if err != nil {
		http.Error(w, "invalid vCard: "+err.Error(), http.StatusBadRequest)
		return
	}
	tag, _, err := resourceNameTag(st, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	msg.Props.Set(tag, name)

	// Replace is delete-then-create: the object store has no in-place message
	// updater, matching how drafts are re-saved.
	if found {
		if err := st.DeleteObject(existing.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if _, err := st.CreateMessage(mapi.PrivateFIDContacts, msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	created, _, err := findObjectByName(st, mapi.PrivateFIDContacts, ".vcf", name)
	if err == nil && created.ChangeNumber != 0 {
		w.Header().Set("ETag", etag(created.ChangeNumber))
	}
	if found {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

// handleDelete removes a contact, honoring If-Match.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, mailbox string) {
	kind, _, name := classify(r.URL.Path)
	if kind != kindObject {
		http.Error(w, "not a contact resource", http.StatusMethodNotAllowed)
		return
	}
	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	obj, found, err := findObjectByName(st, mapi.PrivateFIDContacts, ".vcf", name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if im := r.Header.Get("If-Match"); im != "" && im != etag(obj.ChangeNumber) {
		http.Error(w, "etag mismatch", http.StatusPreconditionFailed)
		return
	}
	// Route to the Recoverable Items dumpster (not a hard purge): the contact leaves
	// the live view but its bumped change number is a sync-collection tombstone.
	if err := st.SoftDeleteObject(obj.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
