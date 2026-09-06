package webmail2api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\nVERSION:4.0\r\n")
	fmt.Fprintf(&b, "FN:%s\r\n", c.Name)
	// N is the structured name: Family ; Given ; Middle ; Prefix ; Suffix.
	// oxvcard imports N's five components to PrSurname/GivenName/MiddleName/
	// DisplayNamePrefix/Generation respectively.
	if c.LastName != "" || c.FirstName != "" || c.MiddleName != "" || c.Prefix != "" || c.Suffix != "" {
		fmt.Fprintf(&b, "N:%s;%s;%s;%s;%s\r\n", c.LastName, c.FirstName, c.MiddleName, c.Prefix, c.Suffix)
	}
	if c.Email != "" {
		fmt.Fprintf(&b, "EMAIL:%s\r\n", c.Email)
	}
	if c.Email2 != "" {
		fmt.Fprintf(&b, "EMAIL:%s\r\n", c.Email2)
	}
	if c.Email3 != "" {
		fmt.Fprintf(&b, "EMAIL:%s\r\n", c.Email3)
	}
	if c.Nickname != "" {
		fmt.Fprintf(&b, "NICKNAME:%s\r\n", c.Nickname)
	}
	if c.Phone != "" {
		fmt.Fprintf(&b, "TEL;TYPE=work:%s\r\n", c.Phone)
	}
	if c.MobilePhone != "" {
		fmt.Fprintf(&b, "TEL;TYPE=CELL:%s\r\n", c.MobilePhone)
	}
	if c.HomePhone != "" {
		fmt.Fprintf(&b, "TEL;TYPE=HOME:%s\r\n", c.HomePhone)
	}
	if c.BusinessFax != "" {
		fmt.Fprintf(&b, "TEL;TYPE=fax,work:%s\r\n", c.BusinessFax)
	}
	if c.Company != "" || c.Department != "" {
		// ORG is semicolon-delimited: company ; department. oxvcard maps ORG's
		// second component to PrDepartmentName, distinct from ROLE (Profession).
		fmt.Fprintf(&b, "ORG:%s;%s\r\n", c.Company, c.Department)
	}
	if c.JobTitle != "" {
		fmt.Fprintf(&b, "TITLE:%s\r\n", c.JobTitle)
	}
	if c.Profession != "" {
		// vCard ROLE maps to PrProfession (not department) in oxvcard.
		fmt.Fprintf(&b, "ROLE:%s\r\n", c.Profession)
	}
	if c.Birthday != "" {
		// vCard BDAY is YYYY-MM-DD; oxvcard's parseBirthday accepts it.
		fmt.Fprintf(&b, "BDAY:%s\r\n", c.Birthday)
	}
	if c.HomeStreet != "" || c.HomeCity != "" || c.HomeState != "" || c.HomePostal != "" || c.HomeCountry != "" {
		// ADR is semicolon-delimited: pobox ; ext ; street ; city ; state ; postal ; country
		fmt.Fprintf(&b, "ADR;TYPE=HOME:;;%s;%s;%s;%s;%s\r\n", c.HomeStreet, c.HomeCity, c.HomeState, c.HomePostal, c.HomeCountry)
	}
	if c.WorkStreet != "" || c.WorkCity != "" || c.WorkState != "" || c.WorkPostal != "" || c.WorkCountry != "" {
		fmt.Fprintf(&b, "ADR;TYPE=WORK:;;%s;%s;%s;%s;%s\r\n", c.WorkStreet, c.WorkCity, c.WorkState, c.WorkPostal, c.WorkCountry)
	}
	if c.OtherStreet != "" || c.OtherCity != "" || c.OtherState != "" || c.OtherPostal != "" || c.OtherCountry != "" {
		fmt.Fprintf(&b, "ADR;TYPE=OTHER:;;%s;%s;%s;%s;%s\r\n", c.OtherStreet, c.OtherCity, c.OtherState, c.OtherPostal, c.OtherCountry)
	}
	if c.IMAddress != "" {
		fmt.Fprintf(&b, "IMPP:%s\r\n", c.IMAddress)
	}
	if c.WebPage != "" {
		fmt.Fprintf(&b, "URL:%s\r\n", c.WebPage)
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
// ones oxvcard emits with TYPE.
func vcardTypedField(vcf []byte, name, typeParam string) string {
	want := strings.ToUpper(typeParam)
	bare := ""
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
		if bare == "" {
			bare = val
		}
	}
	return bare
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
		// A contact group is an IPM.DistList: its members live as JSON in the body,
		// with the group name in PR_SUBJECT.
		if propString(msg, mapi.PrMessageClass) == "IPM.DistList" {
			var body distListBody
			_ = json.Unmarshal([]byte(propString(msg, mapi.PrBody)), &body)
			contacts = append(contacts, contactJSON{
				ID:      strconv.FormatInt(o.ID, 10),
				Name:    propString(msg, mapi.PrSubject),
				IsGroup: true,
				Members: body.Members,
			})
			continue
		}
		vcf, err := oxvcard.Export(msg, opt)
		if err != nil {
			continue
		}
		// ORG is "company;department"; ROLE is Profession (not department).
		orgFields := strings.Split(vcardField(vcf, "ORG"), ";")
		orgAt := func(i int) string {
			if i < len(orgFields) {
				return orgFields[i]
			}
			return ""
		}
		// N is the structured name: Family ; Given ; Middle ; Prefix ; Suffix.
		nameFields := strings.Split(vcardField(vcf, "N"), ";")
		nameAt := func(i int) string {
			if i < len(nameFields) {
				return nameFields[i]
			}
			return ""
		}
		// Home and work addresses are separate ADR lines (TYPE=HOME/WORK), each
		// semicolon-delimited: pobox ; ext ; street ; city ; state ; postal ; country
		homeAdr := strings.Split(vcardTypedField(vcf, "ADR", "HOME"), ";")
		workAdr := strings.Split(vcardTypedField(vcf, "ADR", "WORK"), ";")
		otherAdr := strings.Split(vcardTypedField(vcf, "ADR", "OTHER"), ";")
		adr := func(fields []string, i int) string {
			if i < len(fields) {
				return fields[i]
			}
			return ""
		}
		emails := vcardAll(vcf, "EMAIL")
		emailAt := func(i int) string {
			if i < len(emails) {
				return emails[i]
			}
			return ""
		}
		contacts = append(contacts, contactJSON{
			ID:           strconv.FormatInt(o.ID, 10),
			Name:         vcardField(vcf, "FN"),
			LastName:     nameAt(0),
			FirstName:    nameAt(1),
			MiddleName:   nameAt(2),
			Prefix:       nameAt(3),
			Suffix:       nameAt(4),
			Email:        emailAt(0),
			Email2:       emailAt(1),
			Email3:       emailAt(2),
			Phone:        vcardTypedField(vcf, "TEL", "WORK"), // business telephone
			MobilePhone:  vcardTypedField(vcf, "TEL", "CELL"),
			HomePhone:    vcardTypedField(vcf, "TEL", "HOME"),
			BusinessFax:  vcardTypedField(vcf, "TEL", "FAX"),
			Company:      orgAt(0),
			JobTitle:     vcardField(vcf, "TITLE"),
			Department:   orgAt(1),
			Birthday:     vcardField(vcf, "BDAY"),
			Nickname:     vcardField(vcf, "NICKNAME"),
			FileAs:       fileAsOf(st, msg),
			Profession:   vcardField(vcf, "ROLE"),
			Spouse:       propString(msg, mapi.PrSpouseName),
			HomeStreet:   adr(homeAdr, 2),
			HomeCity:     adr(homeAdr, 3),
			HomeState:    adr(homeAdr, 4),
			HomePostal:   adr(homeAdr, 5),
			HomeCountry:  adr(homeAdr, 6),
			WorkStreet:   adr(workAdr, 2),
			WorkCity:     adr(workAdr, 3),
			WorkState:    adr(workAdr, 4),
			WorkPostal:   adr(workAdr, 5),
			WorkCountry:  adr(workAdr, 6),
			OtherStreet:  adr(otherAdr, 2),
			OtherCity:    adr(otherAdr, 3),
			OtherState:   adr(otherAdr, 4),
			OtherPostal:  adr(otherAdr, 5),
			OtherCountry: adr(otherAdr, 6),
			IMAddress:    vcardField(vcf, "IMPP"),
			WebPage:      vcardField(vcf, "URL"),
			Assistant:    propString(msg, mapi.PrAssistant),
			Manager:      propString(msg, mapi.PrManagerName),
			Office:       propString(msg, mapi.PrOfficeLocation),
			Anniversary:  anniversaryOf(msg),
			Billing:      billingOf(st, msg),
		})
		if cats, err := st.GetCategories(o.ID); err == nil && len(cats) > 0 {
			contacts[len(contacts)-1].Categories = cats
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": contacts, "total": len(contacts)})
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
	// Validated before the old object is deleted, so a rejected edit leaves the
	// contact as it was rather than removing it.
	if err := nameContact(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if propString(msg, mapi.PrMessageClass) != "IPM.DistList" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a distribution list"})
		return
	}
	var body distListBody
	_ = json.Unmarshal([]byte(propString(msg, mapi.PrBody)), &body)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      strconv.FormatInt(id, 10),
		"name":    propString(msg, mapi.PrSubject),
		"members": body.Members,
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
	// oxvcard's vCard path does not carry anniversary/assistant/manager/office, so
	// set them directly as MAPI props after the import (the organizer's rich fields).
	setRichContactProps(st, id, c)
	// Categories ride the shared PidNameKeywords named prop (the same list every
	// protocol reads). Empty list clears any prior categories on a replace.
	_ = st.SetCategories(id, c.Categories)
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

// setRichContactProps stamps the contact's assistant/manager/office/anniversary
// onto the stored message, the fields oxvcard's vCard import does not map. Empty
// values are skipped. Anniversary is a PtSysTime (UnixToNTTime uint64). Billing is
// a PSETID_Common named prop (PidLidBilling), resolved per-store.
func setRichContactProps(st *objectstore.Store, id int64, c contactJSON) {
	var props mapi.PropertyValues
	if c.Assistant != "" {
		props.Set(mapi.PrAssistant, c.Assistant)
	}
	if c.Manager != "" {
		props.Set(mapi.PrManagerName, c.Manager)
	}
	if c.Office != "" {
		props.Set(mapi.PrOfficeLocation, c.Office)
	}
	if c.Anniversary != "" {
		if t, err := time.Parse("2006-01-02", c.Anniversary); err == nil {
			props.Set(mapi.PrWeddingAnniversary, mapi.UnixToNTTime(t))
		}
	}
	if c.Billing != "" {
		if tag, err := billingTag(st, true); err == nil && tag != 0 {
			props.Set(tag, c.Billing)
		}
	}
	if c.FileAs != "" {
		if tag, err := fileAsTag(st, true); err == nil && tag != 0 {
			props.Set(tag, c.FileAs)
		}
	}
	if c.Profession != "" {
		props.Set(mapi.PrProfession, c.Profession)
	}
	if c.Spouse != "" {
		props.Set(mapi.PrSpouseName, c.Spouse)
	}
	if len(props) > 0 {
		_ = st.SetMessageProperties(id, props)
	}
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

// contactPhotoAttachment scans a contact's attachments for the one carrying
// PrAttachmentContactPhoto (the contact's photo) and returns its attach number
// and image bytes. ok is false when the contact has no photo.
func contactPhotoAttachment(msg *oxcmail.Message) (attachNum int32, data []byte, ok bool) {
	for _, att := range msg.Attachments {
		if v, has := att.Props.Get(mapi.PrAttachmentContactPhoto); has {
			if b, isBool := v.(bool); isBool && b {
				if d, isBytes := att.Props.Get(mapi.PrAttachDataBin); isBytes {
					if raw, isRaw := d.([]byte); isRaw {
						if n, has := att.Props.Get(mapi.PrAttachNum); has {
							if num, isInt := n.(int32); isInt {
								return num, raw, true
							}
						}
					}
				}
			}
		}
	}
	return 0, nil, false
}

// handleGetContactPhoto streams the contact's photo attachment bytes. JPEG is
// the canonical format; the content type defaults to image/jpeg.
func (s *Server) handleGetContactPhoto(w http.ResponseWriter, r *http.Request) {
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
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such contact"})
		return
	}
	_, data, has := contactPhotoAttachment(msg)
	if !has {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no photo"})
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	// #nosec G705 -- the daemon stamps X-Content-Type-Options: nosniff and the Content-Type is set explicitly, so the bytes are never interpreted as a document
	_, _ = w.Write(data)
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
	// #nosec G120 -- the request body is already capped by the API's MaxBytesReader, so the multipart parse is bounded before it starts
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected multipart file upload"})
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file field"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read upload"})
		return
	}
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	// The bytes are written straight into the mailbox as an attachment and never
	// pass through delivery, so they are scanned here or not at all, like every
	// other path that stores client-supplied attachment content.
	if mta.ScanStored(s.accounts, c.Email, "contact-photo", data, time.Now()) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the photo was rejected: a virus was detected"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	// Drop an existing photo first so the contact has at most one photo.
	if msg, err := st.OpenMessage(id); err == nil {
		if num, _, has := contactPhotoAttachment(msg); has {
			// #nosec G115 -- the signed and unsigned views of the same 32 bits
			_ = st.DeleteAttachment(id, uint32(num))
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
	if _, _, err := st.CreateAttachment(id, props); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not attach photo"})
		return
	}
	// Mark the contact as having a picture (PidLidHasPicture).
	if tag, err := hasPictureTag(st, true); err == nil && tag != 0 {
		_ = st.SetMessageProperties(id, mapi.PropertyValues{{Tag: tag, Value: true}})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteContactPhoto removes the contact's photo attachment and clears
// PidLidHasPicture.
func (s *Server) handleDeleteContactPhoto(w http.ResponseWriter, r *http.Request) {
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
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such contact"})
		return
	}
	if num, _, has := contactPhotoAttachment(msg); has {
		// #nosec G115 -- the signed and unsigned views of the same 32 bits
		_ = st.DeleteAttachment(id, uint32(num))
	}
	if tag, err := hasPictureTag(st, true); err == nil && tag != 0 {
		_ = st.ModifyMessageProperties(id, mapi.PropertyValues{}, tag)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such contact"})
		return
	}
	// A distribution list exports as a multi-vCard document: one minimal vCard per
	// member (FN + EMAIL), the shape Outlook saves for a personal DL.
	var vcf []byte
	if propString(msg, mapi.PrMessageClass) == "IPM.DistList" {
		var body distListBody
		_ = json.Unmarshal([]byte(propString(msg, mapi.PrBody)), &body)
		var vb strings.Builder
		for _, m := range body.Members {
			vb.WriteString("BEGIN:VCARD\r\nVERSION:4.0\r\n")
			fmt.Fprintf(&vb, "FN:%s\r\nEMAIL:%s\r\n", m, m)
			vb.WriteString("END:VCARD\r\n")
		}
		vcf = []byte(vb.String())
	} else {
		vcf, err = oxvcard.Export(msg, oxvcard.Options{Resolver: st.GetNamedPropIDs})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not export"})
			return
		}
	}
	// Filename: the contact's (or list's) name, sanitized to a safe filename.
	name := propString(msg, mapi.PrDisplayName)
	if name == "" {
		name = propString(msg, mapi.PrSubject)
	}
	var fb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			fb.WriteRune(r)
		}
	}
	filename := fb.String()
	if filename == "" {
		filename = "contact"
	}
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`.vcf"`)
	// #nosec G705 -- the daemon stamps X-Content-Type-Options: nosniff and the Content-Type is set explicitly, so the bytes are never interpreted as a document
	_, _ = w.Write(vcf)
}
