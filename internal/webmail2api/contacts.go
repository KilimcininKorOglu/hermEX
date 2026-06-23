package webmail2api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxvcard"
)

// contactJSON is the SPA's Contact shape.
type contactJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Phone   string `json:"phone,omitempty"`
	Company string `json:"company,omitempty"`
}

// buildVCard renders a minimal vCard 4.0 for the proven oxvcard import path.
func buildVCard(c contactJSON) []byte {
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\nVERSION:4.0\r\n")
	fmt.Fprintf(&b, "FN:%s\r\n", c.Name)
	if c.Email != "" {
		fmt.Fprintf(&b, "EMAIL:%s\r\n", c.Email)
	}
	if c.Phone != "" {
		fmt.Fprintf(&b, "TEL:%s\r\n", c.Phone)
	}
	if c.Company != "" {
		fmt.Fprintf(&b, "ORG:%s\r\n", c.Company)
	}
	b.WriteString("END:VCARD\r\n")
	return []byte(b.String())
}

// vcardField extracts a property value from a vCard, ignoring any parameters.
func vcardField(vcf []byte, name string) string {
	for line := range strings.SplitSeq(string(vcf), "\n") {
		line = strings.TrimRight(line, "\r")
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if semi := strings.IndexByte(key, ';'); semi >= 0 {
			key = key[:semi]
		}
		if strings.EqualFold(key, name) {
			return val
		}
	}
	return ""
}

func (s *Server) handleGetContacts(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	objs, err := st.ListFolderObjects(mapi.PrivateFIDContacts)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"contacts": []contactJSON{}, "total": 0})
		return
	}
	opt := oxvcard.Options{Resolver: st.GetNamedPropIDs}
	contacts := make([]contactJSON, 0, len(objs))
	for _, o := range objs {
		msg, err := st.OpenMessage(o.ID)
		if err != nil {
			continue
		}
		vcf, err := oxvcard.Export(msg, opt)
		if err != nil {
			continue
		}
		org, _, _ := strings.Cut(vcardField(vcf, "ORG"), ";")
		contacts = append(contacts, contactJSON{
			ID:      strconv.FormatInt(o.ID, 10),
			Name:    vcardField(vcf, "FN"),
			Email:   vcardField(vcf, "EMAIL"),
			Phone:   vcardField(vcf, "TEL"),
			Company: org,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": contacts, "total": len(contacts)})
}

func (s *Server) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	var in contactJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	id, err := storeContact(st, in)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save contact"})
		return
	}
	in.ID = strconv.FormatInt(id, 10)
	writeJSON(w, http.StatusOK, map[string]any{"contact": in, "status": "ok"})
}

func (s *Server) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	var in contactJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	// Replace: delete the old object, store the new (its id changes).
	if old, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err == nil {
		_ = st.DeleteObject(old)
	}
	id, err := storeContact(st, in)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save contact"})
		return
	}
	in.ID = strconv.FormatInt(id, 10)
	writeJSON(w, http.StatusOK, map[string]any{"contact": in, "status": "ok"})
}

func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	if err := st.DeleteObject(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete contact"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// storeContact imports the contact as a vCard (the proven CardDAV path) and
// creates it in the Contacts folder, returning the new object id.
func storeContact(st *objectstore.Store, c contactJSON) (int64, error) {
	msg, err := oxvcard.Import(buildVCard(c), oxvcard.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return 0, err
	}
	return st.CreateMessage(mapi.PrivateFIDContacts, msg)
}
