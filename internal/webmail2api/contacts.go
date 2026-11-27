package webmail2api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxvcard"
)

// contactJSON is the SPA's Contact shape. A contact group (is_group) is a named
// list of member addresses, the Outlook personal distribution list. The rich
// fields (title, department, phones, birthday, home address, IM, web page) round-
// trip through oxvcard's vCard import/export, the same path every protocol reads.
type contactJSON struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`                 // FN (formatted name)
	Prefix       string   `json:"prefix,omitempty"`     // PrDisplayNamePrefix (N component 3: Dr./Mr./...)
	FirstName    string   `json:"firstName,omitempty"`  // PrGivenName (N component 1)
	MiddleName   string   `json:"middleName,omitempty"` // PrMiddleName (N component 2)
	LastName     string   `json:"lastName,omitempty"`   // PrSurname (N component 0)
	Suffix       string   `json:"suffix,omitempty"`     // PrGeneration (N component 4: Jr./Sr./...)
	Email        string   `json:"email"`
	Email2       string   `json:"email2,omitempty"` // PidLidEmail2EmailAddress (vCard 2nd EMAIL)
	Email3       string   `json:"email3,omitempty"` // PidLidEmail3EmailAddress (vCard 3rd EMAIL)
	Phone        string   `json:"phone,omitempty"`  // business telephone
	Company      string   `json:"company,omitempty"`
	JobTitle     string   `json:"jobTitle,omitempty"`
	Department   string   `json:"department,omitempty"`
	MobilePhone  string   `json:"mobilePhone,omitempty"`
	HomePhone    string   `json:"homePhone,omitempty"`
	BusinessFax  string   `json:"businessFax,omitempty"` // PrBusinessFaxNumber (vCard TEL;TYPE=fax,work)
	Birthday     string   `json:"birthday,omitempty"`    // YYYY-MM-DD
	Nickname     string   `json:"nickname,omitempty"`    // PrNickname (vCard NICKNAME)
	FileAs       string   `json:"fileAs,omitempty"`      // PidLidFileAs (PSETID_Address named prop)
	Profession   string   `json:"profession,omitempty"`  // PrProfession
	Spouse       string   `json:"spouse,omitempty"`      // PrSpouseName
	HomeStreet   string   `json:"homeStreet,omitempty"`
	HomeCity     string   `json:"homeCity,omitempty"`
	HomeState    string   `json:"homeState,omitempty"`
	HomePostal   string   `json:"homePostal,omitempty"`
	HomeCountry  string   `json:"homeCountry,omitempty"`
	WorkStreet   string   `json:"workStreet,omitempty"`
	WorkCity     string   `json:"workCity,omitempty"`
	WorkState    string   `json:"workState,omitempty"`
	WorkPostal   string   `json:"workPostal,omitempty"`
	WorkCountry  string   `json:"workCountry,omitempty"`
	OtherStreet  string   `json:"otherStreet,omitempty"`
	OtherCity    string   `json:"otherCity,omitempty"`
	OtherState   string   `json:"otherState,omitempty"`
	OtherPostal  string   `json:"otherPostal,omitempty"`
	OtherCountry string   `json:"otherCountry,omitempty"`
	IMAddress    string   `json:"imAddress,omitempty"`
	WebPage      string   `json:"webPage,omitempty"`
	Anniversary  string   `json:"anniversary,omitempty"` // YYYY-MM-DD (PrWeddingAnniversary, direct prop)
	Billing      string   `json:"billing,omitempty"`     // PidLidBilling (PSETID_Common named prop)
	Assistant    string   `json:"assistant,omitempty"`   // PrAssistant
	Manager      string   `json:"manager,omitempty"`     // PrManagerName
	Office       string   `json:"office,omitempty"`      // PrOfficeLocation
	Categories   []string `json:"categories,omitempty"`  // PidNameKeywords, shared category list
	IsGroup      bool     `json:"is_group,omitempty"`
	Members      []string `json:"members,omitempty"`
}

// distListBody is the JSON payload stored in a contact group's message body.
type distListBody struct {
	Members []string `json:"members"`
}

// buildVCard renders a vCard 4.0 for the proven oxvcard import path. The rich
// fields map to vCard properties oxvcard parses into MAPI (TEL/ADR/BDAY/TITLE/
// ROLE/NOTE/URL), so they survive cross-protocol.
func buildVCard(c contactJSON) []byte {
	b := []string{"BEGIN:VCARD\r\nVERSION:4.0\r\n", "FN:" + vcardEscape(c.Name) + "\r\n"}
	// N is the structured name: Family ; Given ; Middle ; Prefix ; Suffix.
	// oxvcard imports N's five components to PrSurname/GivenName/MiddleName/
	// DisplayNamePrefix/Generation respectively.
	b = appendVCardGroup(b, "N:%s;%s;%s;%s;%s\r\n", c.LastName, c.FirstName, c.MiddleName, c.Prefix, c.Suffix)
	for _, line := range vcardSingleLines(c) {
		b = appendVCardLine(b, line.format, line.value)
	}
	b = appendVCardAddresses(b, c)
	b = appendVCardLine(b, "IMPP:%s\r\n", vcardEscape(c.IMAddress))
	b = appendVCardLine(b, "URL:%s\r\n", vcardEscape(c.WebPage))
	b = append(b, "END:VCARD\r\n")
	return []byte(strings.Join(b, ""))
}

// vcardLine pairs a vCard line format with the single value that fills it.
type vcardLine struct {
	format string
	value  string
}

// vcardSingleLines lists the single-valued vCard properties in emission order.
// The parameter spelling of each TEL and the ORG/TITLE/ROLE split are what
// oxvcard reads to pick the MAPI property, so this order and these formats are
// the wire contract.
func vcardSingleLines(c contactJSON) []vcardLine {
	lines := []vcardLine{
		{"EMAIL:%s\r\n", c.Email},
		{"EMAIL:%s\r\n", c.Email2},
		{"EMAIL:%s\r\n", c.Email3},
		{"NICKNAME:%s\r\n", c.Nickname},
		{"TEL;TYPE=work:%s\r\n", c.Phone},
		{"TEL;TYPE=CELL:%s\r\n", c.MobilePhone},
		{"TEL;TYPE=HOME:%s\r\n", c.HomePhone},
		{"TEL;TYPE=fax,work:%s\r\n", c.BusinessFax},
		// ORG is semicolon-delimited: company ; department. oxvcard maps ORG's
		// second component to PrDepartmentName, distinct from ROLE (Profession).
		{"ORG:%s\r\n", vcardGroupValue(c.Company, c.Department)},
		{"TITLE:%s\r\n", c.JobTitle},
		// vCard ROLE maps to PrProfession (not department) in oxvcard.
		{"ROLE:%s\r\n", c.Profession},
		// vCard BDAY is YYYY-MM-DD; oxvcard's parseBirthday accepts it.
		{"BDAY:%s\r\n", c.Birthday},
	}
	for i := range lines {
		// ORG's components are escaped one by one in vcardGroupValue.
		if !strings.HasPrefix(lines[i].format, "ORG:") {
			lines[i].value = vcardEscape(lines[i].value)
		}
	}
	return lines
}

// vcardEscape escapes a text value per RFC 6350 section 3.4: backslash, comma,
// semicolon and line breaks, so a value can neither start a line of its own nor
// move the components of a structured value.
func vcardEscape(s string) string {
	return vcardEscaper.Replace(s)
}

var vcardEscaper = strings.NewReplacer(`\`, `\\`, ",", `\,`, ";", `\;`, "\r\n", `\n`, "\n", `\n`, "\r", `\n`)

// appendVCardAddresses appends the three ADR groups. ADR is semicolon-delimited:
// pobox ; ext ; street ; city ; state ; postal ; country, and the two leading
// components are always empty here.
func appendVCardAddresses(b []string, c contactJSON) []string {
	b = appendVCardGroup(b, "ADR;TYPE=HOME:;;%s;%s;%s;%s;%s\r\n",
		c.HomeStreet, c.HomeCity, c.HomeState, c.HomePostal, c.HomeCountry)
	b = appendVCardGroup(b, "ADR;TYPE=WORK:;;%s;%s;%s;%s;%s\r\n",
		c.WorkStreet, c.WorkCity, c.WorkState, c.WorkPostal, c.WorkCountry)
	return appendVCardGroup(b, "ADR;TYPE=OTHER:;;%s;%s;%s;%s;%s\r\n",
		c.OtherStreet, c.OtherCity, c.OtherState, c.OtherPostal, c.OtherCountry)
}

// appendVCardLine appends one line when its value is set. An empty value emits
// nothing, so oxvcard never imports a blank MAPI property.
func appendVCardLine(b []string, format, value string) []string {
	if value == "" {
		return b
	}
	return append(b, fmt.Sprintf(format, value))
}

// appendVCardGroup appends a structured line when ANY component is set, leaving
// the unset components empty in place. Dropping the line because its first
// component is empty would move every later component into the wrong field on
// import.
func appendVCardGroup(b []string, format string, parts ...string) []string {
	if !anyNonEmpty(parts) {
		return b
	}
	args := make([]any, len(parts))
	for i, p := range parts {
		args[i] = vcardEscape(p)
	}
	return append(b, fmt.Sprintf(format, args...))
}

// vcardGroupValue joins a structured value's components with the vCard
// semicolon, reporting the empty string when every component is unset so
// writeVCardLine drops the line.
func vcardGroupValue(parts ...string) string {
	if !anyNonEmpty(parts) {
		return ""
	}
	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = vcardEscape(p)
	}
	return strings.Join(escaped, ";")
}

// anyNonEmpty reports whether at least one component carries a value.
func anyNonEmpty(parts []string) bool {
	return slices.ContainsFunc(parts, func(s string) bool { return s != "" })
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

// vcardAll extracts every value of a vCard property, in file order, ignoring
// parameters. Used for the multi-valued EMAIL lines that map to Email1/2/3.
func vcardAll(vcf []byte, name string) []string {
	var out []string
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
			out = append(out, val)
		}
	}
	return out
}

// vcardTypedField extracts a property value whose TYPE parameter matches typeParam
// (e.g. "CELL" for TEL;TYPE=CELL). An empty typeParam matches the bare property
// with no TYPE, so the business TEL (no params) is distinct from the mobile/home
// ones oxvcard emits with TYPE. A value of another type never answers, because
// the editor writes every field back and would store that value under this
// type too.
func vcardTypedField(vcf []byte, name, typeParam string) string {
	want := strings.ToUpper(typeParam)
	for line := range strings.SplitSeq(string(vcf), "\n") {
		line = strings.TrimRight(line, "\r")
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		semi := strings.IndexByte(key, ';')
		params := ""
		if semi >= 0 {
			params = key[semi:]
			key = key[:semi]
		}
		if !strings.EqualFold(key, name) {
			continue
		}
		if want == "" && !strings.Contains(strings.ToUpper(params), "TYPE=") {
			return val
		}
		if want != "" && strings.Contains(strings.ToUpper(params), "TYPE="+want) {
			return val
		}
	}
	return ""
}

func (s *Server) handleGetContacts(w http.ResponseWriter, r *http.Request) {
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
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
		// A contact group is an IPM.DistList: its members live as JSON in the body,
		// with the group name in PR_SUBJECT.
		if propString(msg, mapi.PrMessageClass) == "IPM.DistList" {
			contacts = append(contacts, distListContact(o.ID, msg))
			continue
		}
		c, ok := contactFromMessage(st, o.ID, msg, opt)
		if !ok {
			continue
		}
		c.Categories = storedCategories(st, o.ID)
		contacts = append(contacts, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": contacts, "total": len(contacts)})
}

// distListContact renders a contact group for the listing: its members live as
// JSON in the body, with the group name in PR_SUBJECT.
func distListContact(id int64, msg *oxcmail.Message) contactJSON {
	return contactJSON{
		ID:      strconv.FormatInt(id, 10),
		Name:    propString(msg, mapi.PrSubject),
		IsGroup: true,
		Members: distListMembers(id, msg),
	}
}

// contactFromMessage renders one stored contact for the listing, through the
// canonical vCard export so the SPA sees exactly what CardDAV does. It reports
// false when the message cannot be exported.
func contactFromMessage(st *objectstore.Store, id int64, msg *oxcmail.Message, opt oxvcard.Options) (contactJSON, bool) {
	vcf, err := oxvcard.Export(msg, opt)
	if err != nil {
		return contactJSON{}, false
	}
	// ORG is "company;department"; ROLE is Profession (not department).
	org := vcardComponents(vcardField(vcf, "ORG"))
	// N is the structured name: Family ; Given ; Middle ; Prefix ; Suffix.
	name := vcardComponents(vcardField(vcf, "N"))
	emails := vcardAll(vcf, "EMAIL")
	for i := range emails {
		emails[i] = vcardText(emails[i])
	}
	c := contactJSON{
		ID:          strconv.FormatInt(id, 10),
		Name:        vcardText(vcardField(vcf, "FN")),
		LastName:    fieldAt(name, 0),
		FirstName:   fieldAt(name, 1),
		MiddleName:  fieldAt(name, 2),
		Prefix:      fieldAt(name, 3),
		Suffix:      fieldAt(name, 4),
		Email:       fieldAt(emails, 0),
		Email2:      fieldAt(emails, 1),
		Email3:      fieldAt(emails, 2),
		Phone:       vcardText(vcardTypedField(vcf, "TEL", "WORK")), // business telephone
		MobilePhone: vcardText(vcardTypedField(vcf, "TEL", "CELL")),
		HomePhone:   vcardText(vcardTypedField(vcf, "TEL", "HOME")),
		BusinessFax: vcardText(vcardTypedField(vcf, "TEL", "FAX")),
		Company:     fieldAt(org, 0),
		JobTitle:    vcardText(vcardField(vcf, "TITLE")),
		Department:  fieldAt(org, 1),
		Birthday:    vcardField(vcf, "BDAY"),
		Nickname:    vcardText(vcardField(vcf, "NICKNAME")),
		FileAs:      fileAsOf(st, msg),
		Profession:  vcardText(vcardField(vcf, "ROLE")),
		Spouse:      propString(msg, mapi.PrSpouseName),
		IMAddress:   vcardText(vcardField(vcf, "IMPP")),
		WebPage:     vcardText(vcardField(vcf, "URL")),
		Assistant:   propString(msg, mapi.PrAssistant),
		Manager:     propString(msg, mapi.PrManagerName),
		Office:      propString(msg, mapi.PrOfficeLocation),
		Anniversary: anniversaryOf(msg),
		Billing:     billingOf(st, msg),
	}
	setContactAddresses(&c, vcf)
	return c, true
}

// setContactAddresses fills the three address blocks. Home, work and other
// addresses are separate ADR lines (TYPE=HOME/WORK/OTHER), each semicolon-
// delimited: pobox ; ext ; street ; city ; state ; postal ; country.
func setContactAddresses(c *contactJSON, vcf []byte) {
	home := vcardComponents(vcardTypedField(vcf, "ADR", "HOME"))
	work := vcardComponents(vcardTypedField(vcf, "ADR", "WORK"))
	other := vcardComponents(vcardTypedField(vcf, "ADR", "OTHER"))
	c.HomeStreet, c.HomeCity, c.HomeState, c.HomePostal, c.HomeCountry =
		fieldAt(home, 2), fieldAt(home, 3), fieldAt(home, 4), fieldAt(home, 5), fieldAt(home, 6)
	c.WorkStreet, c.WorkCity, c.WorkState, c.WorkPostal, c.WorkCountry =
		fieldAt(work, 2), fieldAt(work, 3), fieldAt(work, 4), fieldAt(work, 5), fieldAt(work, 6)
	c.OtherStreet, c.OtherCity, c.OtherState, c.OtherPostal, c.OtherCountry =
		fieldAt(other, 2), fieldAt(other, 3), fieldAt(other, 4), fieldAt(other, 5), fieldAt(other, 6)
}

// vcardComponents splits a structured value on the semicolons that are not
// escaped and unescapes each component (RFC 6350 section 3.4).
func vcardComponents(raw string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(raw); i++ {
		switch {
		case raw[i] == '\\' && i+1 < len(raw):
			cur.WriteByte(raw[i])
			cur.WriteByte(raw[i+1])
			i++
		case raw[i] == ';':
			out = append(out, vcardText(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(raw[i])
		}
	}
	return append(out, vcardText(cur.String()))
}

// vcardText undoes RFC 6350 text escaping: \n or \N is a line break and any
// other escaped character stands for itself.
func vcardText(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 == len(raw) {
			b.WriteByte(raw[i])
			continue
		}
		i++
		if raw[i] == 'n' || raw[i] == 'N' {
			b.WriteByte('\n')
		} else {
			b.WriteByte(raw[i])
		}
	}
	return b.String()
}

// fieldAt returns one component of a split vCard value, or "" when the value
// carried fewer components than the layout defines.
func fieldAt(fields []string, i int) string {
	if i < len(fields) {
		return fields[i]
	}
	return ""
}

func (s *Server) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	var in contactJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := nameContact(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	if refuseOverQuota(w, st) {
		return
	}
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
	// Validated before the old object is deleted, so a rejected edit leaves the
	// contact as it was rather than removing it.
	if err := nameContact(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	// A numeric id that names an object outside Contacts is refused before the
	// edit can replace it; an id that names nothing still stores a new contact.
	if old, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err == nil {
		if _, err := st.MessageFolder(old); err == nil && !objectInKind(w, st, old, inFolder(mapi.PrivateFIDContacts)) {
			return
		}
	}
	id, err := saveContactEdit(st, r.PathValue("id"), in)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save contact"})
		return
	}
	in.ID = strconv.FormatInt(id, 10)
	writeJSON(w, http.StatusOK, map[string]any{"contact": in, "status": "ok"})
}

// saveContactEdit stores an edit of the contact named by rawID: in place when it
// is a stored contact of the same kind, and otherwise by replacing it, which
// gives the contact a new id.
func saveContactEdit(st *objectstore.Store, rawID string, c contactJSON) (int64, error) {
	old, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return storeContact(st, c)
	}
	done, err := updateContactInPlace(st, old, c)
	if done {
		return old, err
	}
	// A kind change replaces the contact. The replacement is written first, so a
	// failed write leaves the contact as it was. The old object may already be
	// gone (a stale id), and the edit is stored either way, so its delete error
	// is not the caller's failure; it is recorded, since a failed delete leaves
	// the old contact beside its replacement.
	id, err := writeContact(st, c)
	if err != nil {
		return 0, err
	}
	if err := st.DeleteObject(old); err != nil {
		logError("contact-replace-delete", err, logging.Fields{"old": old, "new": id})
	}
	return id, nil
}

// writeContact stores a new contact; a variable only so a test can make the
// write fail.
var writeContact = storeContact

func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	if !objectInKind(w, st, id, inFolder(mapi.PrivateFIDContacts)) {
		return
	}
	if err := st.SoftDeleteObject(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete contact"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleExpandDistList expands a personal distribution list (IPM.DistList contact)
// into its member addresses, the expanddistlist surface the compose recipient picker
// and the reference contactlist module expose. A non-group id returns 400 so the
// caller does not silently treat a regular contact as a one-member list.
func (s *Server) handleExpandDistList(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if propString(msg, mapi.PrMessageClass) != "IPM.DistList" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a distribution list"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      strconv.FormatInt(id, 10),
		"name":    propString(msg, mapi.PrSubject),
		"members": distListMembers(id, msg),
	})
}

// storeContact imports the contact as a vCard (the proven CardDAV path) and
// creates it in the Contacts folder, returning the new object id.
// nameContact fills in the contact's name and refuses one that has none to fill
// in from. A contact stored with an empty name is not rejected anywhere further
// down: vCard requires an FN, so the export substitutes a placeholder, and the
// contact then reads back under that placeholder in every client. A caller whose
// request named the wrong field sees a saved contact called "Unknown" and no
// error at all.
//
// The name falls back to the structured first and last name, then to the first
// e-mail address, which is what a mail client files a nameless address under.
func nameContact(c *contactJSON) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		c.Name = strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName))
	}
	if c.Name == "" && !c.IsGroup {
		for _, addr := range []string{c.Email, c.Email2, c.Email3} {
			if addr = strings.TrimSpace(addr); addr != "" {
				c.Name = addr
				break
			}
		}
	}
	if c.Name == "" {
		if c.IsGroup {
			return errors.New("a contact group needs a name")
		}
		return errors.New("a contact needs a name or an email address")
	}
	return nil
}

func storeContact(st *objectstore.Store, c contactJSON) (int64, error) {
	// A contact group has no vCard contact shape; store it as an IPM.DistList with
	// the members as a JSON body (the shape handleGetContacts reads back).
	if c.IsGroup {
		return storeJSONItem(st, mapi.PrivateFIDContacts, "IPM.DistList", c.Name, distListBody{Members: c.Members})
	}
	msg, err := oxvcard.Import(buildVCard(c), oxvcard.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return 0, err
	}
	id, err := st.CreateMessage(mapi.PrivateFIDContacts, msg)
	if err != nil {
		return 0, err
	}
	// The contact is stored; a follow-up write that fails must not report the
	// create as failed, or a retry stores it twice. Each failure is recorded so
	// the operator sees the field that was lost.
	//
	// oxvcard's vCard path does not carry anniversary/assistant/manager/office, so
	// set them directly as MAPI props after the import (the organizer's rich fields).
	if rich := richContactProps(st, c); len(rich) > 0 {
		if err := st.SetMessageProperties(id, rich); err != nil {
			st.LogSwallowedError("contact.rich_props", err)
		}
	}
	// Categories ride the shared PidNameKeywords named prop (the same list every
	// protocol reads). Empty list clears any prior categories on a replace.
	if err := st.SetCategories(id, c.Categories); err != nil {
		st.LogSwallowedError("contact.categories", err)
	}
	return id, nil
}

// anniversaryOf reads PrWeddingAnniversary (a PtSysTime, uint64 NT time) and
// formats it as YYYY-MM-DD, or "" when unset.
func anniversaryOf(msg *oxcmail.Message) string {
	if v, ok := msg.Props.Get(mapi.PrWeddingAnniversary); ok {
		if n, ok := v.(uint64); ok && n != 0 {
			return mapi.NTTimeToUnix(n).Format("2006-01-02")
		}
	}
	return ""
}

// richContactProps returns the contact's assistant/manager/office/anniversary,
// the fields oxvcard's vCard import does not map. Empty values are skipped.
// Anniversary is a PtSysTime (UnixToNTTime uint64). Billing is a PSETID_Common
// named prop (PidLidBilling), resolved per-store.
func richContactProps(st *objectstore.Store, c contactJSON) mapi.PropertyValues {
	var props mapi.PropertyValues
	for _, f := range []struct {
		tag   mapi.PropTag
		value string
	}{
		{mapi.PrAssistant, c.Assistant},
		{mapi.PrManagerName, c.Manager},
		{mapi.PrOfficeLocation, c.Office},
		{mapi.PrProfession, c.Profession},
		{mapi.PrSpouseName, c.Spouse},
	} {
		setIfPresent(&props, f.tag, f.value)
	}
	setAnniversary(&props, c.Anniversary)
	setNamedString(&props, st, billingTag, c.Billing)
	setNamedString(&props, st, fileAsTag, c.FileAs)
	return props
}

// contactProps builds every stored property of a contact from its JSON form:
// the oxvcard import of its vCard plus the fields that import does not map.
func contactProps(st *objectstore.Store, c contactJSON) (mapi.PropertyValues, error) {
	msg, err := oxvcard.Import(buildVCard(c), oxvcard.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return nil, err
	}
	props := msg.Props
	for _, p := range richContactProps(st, c) {
		props.Set(p.Tag, p.Value)
	}
	return props, nil
}

// managedContactTags returns every property the webmail contact editor can set,
// found by building a contact with every field filled. An edit that leaves one
// of these out has cleared that field; any other property on the stored contact
// belongs to another client (or to the photo) and is left alone.
func managedContactTags(st *objectstore.Store) (map[mapi.PropTag]bool, error) {
	full := contactJSON{
		Name: "x", Prefix: "x", FirstName: "x", MiddleName: "x", LastName: "x", Suffix: "x",
		Email: "a@x.invalid", Email2: "b@x.invalid", Email3: "c@x.invalid",
		Phone: "x", Company: "x", JobTitle: "x", Department: "x", MobilePhone: "x", HomePhone: "x",
		BusinessFax: "x", Birthday: "2000-01-02", Nickname: "x", FileAs: "x", Profession: "x", Spouse: "x",
		HomeStreet: "x", HomeCity: "x", HomeState: "x", HomePostal: "x", HomeCountry: "x",
		WorkStreet: "x", WorkCity: "x", WorkState: "x", WorkPostal: "x", WorkCountry: "x",
		OtherStreet: "x", OtherCity: "x", OtherState: "x", OtherPostal: "x", OtherCountry: "x",
		IMAddress: "x", WebPage: "x", Anniversary: "2000-01-02", Billing: "x", Assistant: "x",
		Manager: "x", Office: "x",
	}
	props, err := contactProps(st, full)
	if err != nil {
		return nil, err
	}
	tags := make(map[mapi.PropTag]bool, len(props))
	for _, p := range props {
		tags[p.Tag] = true
	}
	return tags, nil
}

// clearedTags returns the managed tags an edit's property set leaves out.
func clearedTags(managed map[mapi.PropTag]bool, props mapi.PropertyValues) []mapi.PropTag {
	var out []mapi.PropTag
	for tag := range managed {
		if !props.Has(tag) {
			out = append(out, tag)
		}
	}
	return out
}

// updateContactInPlace writes an edit onto the stored contact id, keeping its
// id, its photo and every property the editor does not model, which a
// delete-and-recreate would drop. It reports false, having changed nothing, when
// id is not a stored contact of the same kind (a contact turned into a group or
// back), which the caller then replaces.
func updateContactInPlace(st *objectstore.Store, id int64, c contactJSON) (bool, error) {
	msg, err := st.OpenMessage(id)
	if err != nil {
		return false, nil
	}
	class := propString(msg, mapi.PrMessageClass)
	if c.IsGroup {
		if class != "IPM.DistList" {
			return false, nil
		}
		return true, updateDistList(st, id, c)
	}
	if !strings.EqualFold(class, "IPM.Contact") {
		return false, nil
	}
	props, err := contactProps(st, c)
	if err != nil {
		return true, err
	}
	managed, err := managedContactTags(st)
	if err != nil {
		return true, err
	}
	if err := st.ModifyMessageProperties(id, props, clearedTags(managed, props)...); err != nil {
		return true, err
	}
	return true, st.SetCategories(id, c.Categories)
}

// updateDistList rewrites a contact group's name and members in place.
func updateDistList(st *objectstore.Store, id int64, c contactJSON) error {
	b, err := json.Marshal(distListBody{Members: c.Members})
	if err != nil {
		return err
	}
	var props mapi.PropertyValues
	props.Set(mapi.PrSubject, c.Name)
	props.Set(mapi.PrBody, string(b))
	return st.SetMessageProperties(id, props)
}

// setIfPresent stamps a string property, skipping an empty value.
func setIfPresent(props *mapi.PropertyValues, tag mapi.PropTag, value string) {
	if value != "" {
		props.Set(tag, value)
	}
}

// setAnniversary stamps PrWeddingAnniversary, a PtSysTime, from a YYYY-MM-DD
// date. An unparseable date is skipped rather than stored as a zero time.
func setAnniversary(props *mapi.PropertyValues, date string) {
	if date == "" {
		return
	}
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return
	}
	props.Set(mapi.PrWeddingAnniversary, mapi.UnixToNTTime(t))
}

// setNamedString stamps a value under a named property, allocating its id in
// this store. An unresolvable name drops the value rather than failing the save.
func setNamedString(props *mapi.PropertyValues, st *objectstore.Store,
	resolve func(*objectstore.Store, bool) (mapi.PropTag, error), value string) {
	if value == "" {
		return
	}
	tag, err := resolve(st, true)
	if err != nil || tag == 0 {
		return
	}
	props.Set(tag, value)
}

// billingTag resolves PidLidBilling (NameBilling, PSETID_Common) to a PtUnicode
// tag for this store, allocating its id when create is set (idempotent).
func billingTag(st *objectstore.Store, create bool) (mapi.PropTag, error) {
	ids, err := st.GetNamedPropIDs(create, []mapi.PropertyName{mapi.NameBilling})
	if err != nil || len(ids) == 0 || ids[0] == 0 {
		return 0, err
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtUnicode)), nil
}

// billingOf reads PidLidBilling from the contact's stored properties. The tag is
// resolved read-only (no allocation); an unresolved named prop yields "".
func billingOf(st *objectstore.Store, msg *oxcmail.Message) string {
	tag, err := billingTag(st, false)
	if err != nil || tag == 0 {
		return ""
	}
	return propString(msg, tag)
}

// fileAsTag resolves PidLidFileAs (NameFileAs, PSETID_Address) to a PtUnicode tag
// for this store, allocating its id when create is set.
func fileAsTag(st *objectstore.Store, create bool) (mapi.PropTag, error) {
	ids, err := st.GetNamedPropIDs(create, []mapi.PropertyName{mapi.NameFileAs})
	if err != nil || len(ids) == 0 || ids[0] == 0 {
		return 0, err
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtUnicode)), nil
}

// fileAsOf reads PidLidFileAs from the contact's stored properties.
func fileAsOf(st *objectstore.Store, msg *oxcmail.Message) string {
	tag, err := fileAsTag(st, false)
	if err != nil || tag == 0 {
		return ""
	}
	return propString(msg, tag)
}

// hasPictureTag resolves PidLidHasPicture (NameHasPicture, PSETID_Address) to a
// PtBoolean tag for this store, allocating its id when create is set.
func hasPictureTag(st *objectstore.Store, create bool) (mapi.PropTag, error) {
	ids, err := st.GetNamedPropIDs(create, []mapi.PropertyName{mapi.NameHasPicture})
	if err != nil || len(ids) == 0 || ids[0] == 0 {
		return 0, err
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtBoolean)), nil
}

// markHasPicture sets PidLidHasPicture on a contact. The named property is
// allocated on demand, so a zero tag after an allocating lookup means the store
// refused it, which is a failure rather than a flag with nowhere to go.
func markHasPicture(st *objectstore.Store, id int64) error {
	tag, err := hasPictureTag(st, true)
	if err != nil {
		return err
	}
	if tag == 0 {
		return errors.New("webmail2api: no named property id for PidLidHasPicture")
	}
	return st.SetMessageProperties(id, mapi.PropertyValues{{Tag: tag, Value: true}})
}

// handleGetContactPhoto streams the contact's photo attachment bytes. JPEG is
// the canonical format; the content type defaults to image/jpeg.
func (s *Server) handleGetContactPhoto(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such contact"})
		return
	}
	_, data, has := objectstore.ContactPhotoAttachment(msg)
	if !has {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no photo"})
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	// #nosec G705 -- the daemon stamps X-Content-Type-Options: nosniff and the Content-Type is set explicitly, so the bytes are never interpreted as a document
	writeBody(w, data)
}

// handleSetContactPhoto replaces the contact's photo. A prior photo (if any) is
// deleted first so only one photo attachment exists. The photo is stored as a
// by-value JPEG attachment flagged PrAttachmentContactPhoto, and the contact's
// PidLidHasPicture is set true so Outlook and the GAL see it.
func (s *Server) handleSetContactPhoto(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	data, ok := s.readScannedPhoto(w, r)
	if !ok {
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	if !objectInKind(w, st, id, inFolder(mapi.PrivateFIDContacts)) || refuseOverQuota(w, st) {
		return
	}
	if err := replaceContactPhoto(st, id, data); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not attach photo"})
		return
	}
	// Mark the contact as having a picture (PidLidHasPicture); a client that reads
	// the flag rather than the attachment shows no photo without it. Replacing
	// the photo is idempotent, so a failure here is reported and the client may
	// simply retry.
	if err := markHasPicture(st, id); err != nil {
		logError("contact-photo-flag", err, nil)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not attach photo"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// replaceContactPhoto stores data as the contact's one photo attachment, dropping
// an existing photo first so the contact has at most one.
func replaceContactPhoto(st *objectstore.Store, id int64, data []byte) error {
	// A contact that cannot be read, or an old photo that cannot be dropped,
	// fails the upload: adding the new one would leave the contact with two.
	msg, err := st.OpenMessage(id)
	if err != nil {
		return err
	}
	if num, _, has := objectstore.ContactPhotoAttachment(msg); has {
		// #nosec G115 -- the signed and unsigned views of the same 32 bits
		if err := st.DeleteAttachment(id, uint32(num)); err != nil {
			return err
		}
	}
	props := mapi.PropertyValues{
		{Tag: mapi.PrAttachDataBin, Value: data},
		{Tag: mapi.PrAttachFilename, Value: "ContactPicture.jpg"},
		{Tag: mapi.PrAttachLongFilename, Value: "ContactPicture.jpg"},
		{Tag: mapi.PrAttachExtension, Value: ".jpg"},
		{Tag: mapi.PrDisplayName, Value: "ContactPicture.jpg"},
		{Tag: mapi.PrRenderingPosition, Value: int32(-1)},
		{Tag: mapi.PrAttachmentContactPhoto, Value: true},
	}
	_, _, err = st.CreateAttachment(id, props)
	return err
}

// readScannedPhoto reads the uploaded photo out of the multipart body and
// virus-scans it, answering the client itself on every failure.
//
// The bytes are written straight into the mailbox as an attachment and never
// pass through delivery, so they are scanned here or not at all, like every
// other path that stores client-supplied attachment content.
func (s *Server) readScannedPhoto(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	// #nosec G120 -- the request body is already capped by the API's MaxBytesReader, so the multipart parse is bounded before it starts
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected multipart file upload"})
		return nil, false
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file field"})
		return nil, false
	}
	data, err := io.ReadAll(file)
	if cerr := file.Close(); cerr != nil {
		logError("contact-photo-upload-close", cerr, logging.Fields{})
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read upload"})
		return nil, false
	}
	// An empty file is no photo; storing it would replace a real one with nothing.
	if len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the photo is empty"})
		return nil, false
	}
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil, false
	}
	if mta.ScanStored(s.accounts, c.Email, "contact-photo", data, time.Now()) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the photo was rejected: a virus was detected"})
		return nil, false
	}
	return data, true
}

// handleDeleteContactPhoto removes the contact's photo attachment and clears
// PidLidHasPicture.
func (s *Server) handleDeleteContactPhoto(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	if !objectInKind(w, st, id, inFolder(mapi.PrivateFIDContacts)) {
		return
	}
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such contact"})
		return
	}
	if err := clearContactPhoto(st, id, msg); err != nil {
		logError("contact-photo-delete", err, logging.Fields{"id": id})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not remove photo"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// clearContactPhoto drops the contact's photo attachment and clears
// PidLidHasPicture, returning the first failure so a photo that is still there
// is never answered as removed.
func clearContactPhoto(st *objectstore.Store, id int64, msg *oxcmail.Message) error {
	if num, _, has := objectstore.ContactPhotoAttachment(msg); has {
		// #nosec G115 -- the signed and unsigned views of the same 32 bits
		if err := st.DeleteAttachment(id, uint32(num)); err != nil {
			return err
		}
	}
	tag, err := hasPictureTag(st, true)
	if err != nil {
		return err
	}
	if tag == 0 {
		return nil
	}
	return st.ModifyMessageProperties(id, mapi.PropertyValues{}, tag)
}

// handleExportContact streams a contact as a vCard 4.0 download (.vcf). It uses
// oxvcard's Export (the canonical CardDAV path), so the file a user saves here
// is byte-identical to what CardDAV, EAS, and EWS see.
func (s *Server) handleExportContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such contact"})
		return
	}
	// A distribution list exports as a multi-vCard document: one minimal vCard per
	// member (FN + EMAIL), the shape Outlook saves for a personal DL.
	var vcf []byte
	if propString(msg, mapi.PrMessageClass) == "IPM.DistList" {
		vcf = distListVCards(id, msg)
	} else {
		vcf, err = oxvcard.Export(msg, oxvcard.Options{Resolver: st.GetNamedPropIDs})
		if err != nil {
			logError("contact-export", err, logging.Fields{"id": id})
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not export"})
			return
		}
	}
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+contactFilename(msg)+`.vcf"`)
	// #nosec G705 -- the daemon stamps X-Content-Type-Options: nosniff and the Content-Type is set explicitly, so the bytes are never interpreted as a document
	writeBody(w, vcf)
}

// distListVCards renders a distribution list as one minimal vCard per member.
func distListVCards(id int64, msg *oxcmail.Message) []byte {
	var out []byte
	for _, m := range distListMembers(id, msg) {
		out = append(out, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:"+m+"\r\nEMAIL:"+m+"\r\nEND:VCARD\r\n"...)
	}
	return out
}

// distListMembers decodes a contact group's members from the JSON body webmail
// writes. A group another client wrote (Outlook keeps its notes in the body) has
// no such list, so it shows no members rather than failing; the body that did not
// decode is recorded.
func distListMembers(id int64, msg *oxcmail.Message) []string {
	text := propString(msg, mapi.PrBody)
	if text == "" {
		return nil
	}
	var body distListBody
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		logError("contact-group-body", err, logging.Fields{"id": id})
		return nil
	}
	return body.Members
}

// contactFilename is the contact's (or list's) name reduced to the characters
// that are safe in a Content-Disposition filename, falling back to "contact"
// when nothing survives.
func contactFilename(msg *oxcmail.Message) string {
	name := propString(msg, mapi.PrDisplayName)
	if name == "" {
		name = propString(msg, mapi.PrSubject)
	}
	var kept []rune
	for _, r := range name {
		if isSafeFilenameRune(r) {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return "contact"
	}
	return string(kept)
}

// isSafeFilenameRune reports whether a rune may appear in a download filename.
func isSafeFilenameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	default:
		return r == '-' || r == '_'
	}
}
