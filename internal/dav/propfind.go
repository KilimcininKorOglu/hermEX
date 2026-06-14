package dav

import (
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// handlePropfind answers a PROPFIND. It walks the discovery chain a CardDAV
// client follows — root (current-user-principal) -> principal
// (addressbook-home-set) -> home set -> the Contacts address book — and, at
// Depth 1 on the address book, lists its member vCards. The requested property
// set is not filtered: a useful standard set is returned for each resource (a
// documented v1 simplification; clients ignore properties they did not ask for).
func (s *Server) handlePropfind(w http.ResponseWriter, r *http.Request, user, mailbox string) {
	kind, pathUser, _ := classify(r.URL.Path)
	if pathUser == "" {
		pathUser = user
	}
	depth := r.Header.Get("Depth")

	var responses []msResponse
	switch kind {
	case kindRoot:
		responses = []msResponse{principalLink(r.URL.Path, pathUser)}
	case kindPrincipal:
		responses = []msResponse{principalResponse(pathUser)}
	case kindHomeSet:
		responses = []msResponse{homeSetResponse(pathUser)}
		if depth != "0" {
			rs, err := s.addressbookResponses(mailbox, pathUser, depth)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// At the home set, Depth 1 lists the address books it contains, not
			// their members.
			responses = append(responses, rs[0])
		}
	case kindAddressbook:
		rs, err := s.addressbookResponses(mailbox, pathUser, depth)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		responses = rs
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	writeMultistatus(w, &multistatus{Responses: responses})
}

// principalLink answers a root PROPFIND with the current-user-principal, the
// entry point of CardDAV discovery.
func principalLink(path, user string) msResponse {
	return msResponse{
		Href: path,
		Propstat: []msPropstat{{
			Prop:   msProp{CurrentUserPrOne: &href{Href: principalPath(user)}},
			Status: statusOK,
		}},
	}
}

// principalResponse describes the user principal: its URL and the
// addressbook-home-set that leads to the address books.
func principalResponse(user string) msResponse {
	return msResponse{
		Href: principalPath(user),
		Propstat: []msPropstat{{
			Prop: msProp{
				ResourceType:       &resourceType{Principal: empty},
				DisplayName:        user,
				PrincipalURL:       &href{Href: principalPath(user)},
				AddressbookHomeSet: &href{Href: homeSetPath(user)},
				CurrentUserPrOne:   &href{Href: principalPath(user)},
			},
			Status: statusOK,
		}},
	}
}

// homeSetResponse describes the addressbook-home-set collection.
func homeSetResponse(user string) msResponse {
	return msResponse{
		Href: homeSetPath(user),
		Propstat: []msPropstat{{
			Prop: msProp{
				ResourceType: &resourceType{Collection: empty},
				DisplayName:  "Address Books",
			},
			Status: statusOK,
		}},
	}
}

// addressbookResponses returns the Contacts address-book collection response,
// followed (when depth != "0") by one response per member vCard.
func (s *Server) addressbookResponses(mailbox, user, depth string) ([]msResponse, error) {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	max, err := st.FolderMaxChangeNumber(mapi.PrivateFIDContacts)
	if err != nil {
		return nil, err
	}
	coll := msResponse{
		Href: addressbookPath(user),
		Propstat: []msPropstat{{
			Prop: msProp{
				ResourceType: collectionResourceType(),
				DisplayName:  "Contacts",
				GetCTag:      ctag(max),
				SyncToken:    syncToken(max),
			},
			Status: statusOK,
		}},
	}
	responses := []msResponse{coll}
	if depth == "0" {
		return responses, nil
	}

	objs, err := st.ListFolderObjects(mapi.PrivateFIDContacts)
	if err != nil {
		return nil, err
	}
	for _, o := range objs {
		responses = append(responses, msResponse{
			Href: objectPath(user, resourceName(st, o.ID)),
			Propstat: []msPropstat{{
				Prop: msProp{
					GetETag:        etag(o.ChangeNumber),
					GetContentType: "text/vcard; charset=utf-8",
				},
				Status: statusOK,
			}},
		})
	}
	return responses, nil
}

// writeMultistatus serializes and writes a 207 Multistatus response.
func writeMultistatus(w http.ResponseWriter, ms *multistatus) {
	body, err := marshalMultistatus(ms)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", `application/xml; charset=utf-8`)
	w.WriteHeader(http.StatusMultiStatus)
	w.Write(body)
}

// etag is a quoted entity tag derived from an object's change number.
func etag(cn uint64) string { return `"` + strconv.FormatUint(cn, 10) + `"` }

// ctag is a collection tag: the highest member change number, opaque to clients.
func ctag(max uint64) string { return strconv.FormatUint(max, 10) }

// syncToken is an opaque RFC 6578 sync token carrying the collection's change
// high-water mark.
func syncToken(max uint64) string { return "hermex:sync:" + strconv.FormatUint(max, 10) }

// resourceName is the last path segment of an object's URL. It falls back to the
// object EID; the object lifecycle increment stores the client-chosen name as a
// property and prefers it here.
func resourceName(st *objectstore.Store, id int64) string {
	return strconv.FormatInt(id, 10) + ".vcf"
}
