package dav

import (
	"io"
	"net/http"
	"slices"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
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

// objectCacheControl marks a GET of one calendar or contact object uncacheable.
// The body is the account's own data, and a browser or proxy that kept it could
// hand it to a later user. The ETag stays: DAV clients use it for sync and
// If-Match, not for HTTP caching.
const objectCacheControl = "no-store"

// handleGet serves a contact as a vCard. HEAD returns the same headers with no
// body.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, mailbox string) {
	st, fid, name, ok := s.openObjectCollection(w, r, mailbox, cardTarget, false)
	if !ok {
		return
	}
	defer st.Close()

	obj, found, err := findObjectByName(st, fid, ".vcf", name)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	msg, err := st.OpenMessage(obj.ID)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	vcf, err := oxvcard.Export(msg, vcardOptions(st))
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("ETag", etag(obj.ChangeNumber))
	w.Header().Set("Cache-Control", objectCacheControl)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	// #nosec G705 -- the daemon stamps X-Content-Type-Options: nosniff and the Content-Type is set explicitly, so the bytes are never interpreted as a document
	_, _ = w.Write(vcf) // final response body; a failed write means the client is gone
}

// handlePut creates or replaces a contact from a vCard body. It honors
// If-None-Match: * (create-only) and If-Match (replace-guard), responding 201 on
// create and 204 on replace with the new ETag.
func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, mailbox string) {
	st, fid, name, ok := s.openObjectCollection(w, r, mailbox, cardTarget, true)
	if !ok {
		return
	}
	defer st.Close()

	existing, found, err := findObjectByName(st, fid, ".vcf", name)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	if failure, status := etagPrecondition(r, existing, found); failure != "" {
		http.Error(w, failure, status)
		return
	}

	msg, status, err := importVCardBody(st, io.LimitReader(r.Body, s.vcardLimit()), name)
	if err != nil {
		s.davError(w, err, status)
		return
	}

	if err := replaceContact(st, fid, msg, existing, found); err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}

	created, _, err := findObjectByName(st, fid, ".vcf", name)
	if err == nil && created.ChangeNumber != 0 {
		w.Header().Set("ETag", etag(created.ChangeNumber))
	}
	if found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// importVCardBody reads a PUT body as a vCard and stamps the client-chosen resource
// name on the message, so a later GET of that URL resolves back to it. On failure it
// also reports the HTTP status to answer with.
func importVCardBody(st *objectstore.Store, body io.Reader, name string) (*oxcmail.Message, int, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	msg, err := oxvcard.Import(raw, vcardOptions(st))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	tag, _, err := resourceNameTag(st, true)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	msg.Props.Set(tag, name)
	return msg, 0, nil
}

// etagPrecondition evaluates the conditional headers an ordinary DAV PUT carries.
// A non-empty message is the failure to report, at the given status.
func etagPrecondition(r *http.Request, existing objectstore.FolderObject, found bool) (string, int) {
	if r.Header.Get("If-None-Match") == "*" && found {
		return "already exists", http.StatusPreconditionFailed
	}
	if im := r.Header.Get("If-Match"); im != "" {
		if !found || im != etag(existing.ChangeNumber) {
			return "etag mismatch", http.StatusPreconditionFailed
		}
	}
	return "", 0
}

// replaceContact stores a vCard PUT in place. A card with a PHOTO replaces the
// contact's picture. A card without one keeps the stored picture and its
// has-picture flag, because a client that cannot carry a photo would otherwise
// erase the one Outlook or webmail set.
func replaceContact(st *objectstore.Store, fid int64, msg *oxcmail.Message, existing objectstore.FolderObject, found bool) error {
	managed, err := oxvcard.ManagedTags(vcardOptions(st))
	if err != nil {
		return err
	}
	if found && len(msg.Attachments) == 0 {
		if id := hasPictureID(st); id != 0 {
			managed = slices.DeleteFunc(managed, func(tag mapi.PropTag) bool { return tag.ID() == id })
		}
	}
	if err := replaceObject(st, fid, msg, existing, found, managed); err != nil || !found {
		return err
	}
	return replacePhoto(st, existing.ID, msg.Attachments)
}

// hasPictureID is the store's id for PidLidHasPicture, or 0 when it has none.
func hasPictureID(st *objectstore.Store) uint16 {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameHasPicture})
	if err != nil || len(ids) != 1 {
		return 0
	}
	return ids[0]
}

// replacePhoto swaps the contact's stored picture for the one a card carries. A
// card without a photo leaves the stored one alone.
func replacePhoto(st *objectstore.Store, id int64, photos []oxcmail.Attachment) error {
	if len(photos) == 0 {
		return nil
	}
	stored, err := st.OpenMessage(id)
	if err != nil {
		return err
	}
	if num, ok := storedPhotoNum(stored); ok {
		if err := st.DeleteAttachment(id, num); err != nil {
			return err
		}
	}
	for _, att := range photos {
		if _, _, err := st.CreateAttachment(id, att.Props); err != nil {
			return err
		}
	}
	return nil
}

// storedPhotoNum is the attachment number of a stored contact's picture.
func storedPhotoNum(stored *oxcmail.Message) (uint32, bool) {
	att, ok := oxvcard.PhotoAttachment(stored)
	if !ok {
		return 0, false
	}
	v, _ := att.Props.Get(mapi.PrAttachNum)
	n, ok := v.(int32)
	// #nosec G115 -- the signed and unsigned views of the same 32 bits
	return uint32(n), ok
}

// replaceObject stores a PUT body. With no object under the name it creates one.
// Otherwise it writes the body over the existing object, which keeps its message
// id and every property and attachment the body's format does not model: the
// managed properties the body sets are replaced and those it no longer sets are
// removed.
func replaceObject(st *objectstore.Store, fid int64, msg *oxcmail.Message, existing objectstore.FolderObject, found bool, managed []mapi.PropTag) error {
	if !found {
		_, err := st.CreateMessage(fid, msg)
		return err
	}
	var absent []mapi.PropTag
	for _, tag := range managed {
		if !msg.Props.Has(tag) {
			absent = append(absent, tag)
		}
	}
	return st.ModifyMessageProperties(existing.ID, msg.Props, absent...)
}

// handleDelete removes a contact, honoring If-Match.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, mailbox string) {
	st, fid, name, ok := s.openObjectCollection(w, r, mailbox, cardTarget, false)
	if !ok {
		return
	}
	defer st.Close()

	obj, found, err := findObjectByName(st, fid, ".vcf", name)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
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
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
