package webmail2api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxvcard"
)

// contactJSON is the SPA's Contact shape. A contact group (is_group) is a named
// list of member addresses, the Outlook personal distribution list. The rich
// fields (title, department, phones, birthday, home address, IM, web page) round-
// trip through oxvcard's vCard import/export, the same path every protocol reads.
type contactJSON struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Email         string   `json:"email"`
	Phone         string   `json:"phone,omitempty"`         // business telephone
	Company       string   `json:"company,omitempty"`
	JobTitle      string   `json:"jobTitle,omitempty"`
	Department    string   `json:"department,omitempty"`
	MobilePhone   string   `json:"mobilePhone,omitempty"`
	HomePhone     string   `json:"homePhone,omitempty"`
	Birthday      string   `json:"birthday,omitempty"`      // YYYY-MM-DD
	HomeStreet    string   `json:"homeStreet,omitempty"`
	HomeCity      string   `json:"homeCity,omitempty"`
	HomeState     string   `json:"homeState,omitempty"`
	HomePostal    string   `json:"homePostal,omitempty"`
	HomeCountry   string   `json:"homeCountry,omitempty"`
	WorkStreet    string   `json:"workStreet,omitempty"`
	WorkCity      string   `json:"workCity,omitempty"`
	WorkState     string   `json:"workState,omitempty"`
	WorkPostal    string   `json:"workPostal,omitempty"`
	WorkCountry   string   `json:"workCountry,omitempty"`
	IMAddress     string   `json:"imAddress,omitempty"`
	WebPage       string   `json:"webPage,omitempty"`
	Anniversary   string   `json:"anniversary,omitempty"`   // YYYY-MM-DD (PrWeddingAnniversary, direct prop)
	Assistant     string   `json:"assistant,omitempty"`     // PrAssistant
	Manager       string   `json:"manager,omitempty"`       // PrManagerName
	Office        string   `json:"office,omitempty"`        // PrOfficeLocation
	IsGroup       bool     `json:"is_group,omitempty"`
	Members       []string `json:"members,omitempty"`
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
	if c.Email != "" {
		fmt.Fprintf(&b, "EMAIL:%s\r\n", c.Email)
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
	if c.Company != "" {
		fmt.Fprintf(&b, "ORG:%s\r\n", c.Company)
	}
	if c.JobTitle != "" {
		fmt.Fprintf(&b, "TITLE:%s\r\n", c.JobTitle)
	}
	if c.Department != "" {
		fmt.Fprintf(&b, "ROLE:%s\r\n", c.Department)
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
		org, _, _ := strings.Cut(vcardField(vcf, "ORG"), ";")
		// Home and work addresses are separate ADR lines (TYPE=HOME/WORK), each
		// semicolon-delimited: pobox ; ext ; street ; city ; state ; postal ; country
		homeAdr := strings.Split(vcardTypedField(vcf, "ADR", "HOME"), ";")
		workAdr := strings.Split(vcardTypedField(vcf, "ADR", "WORK"), ";")
		adr := func(fields []string, i int) string {
			if i < len(fields) {
				return fields[i]
			}
			return ""
		}
		contacts = append(contacts, contactJSON{
			ID:          strconv.FormatInt(o.ID, 10),
			Name:        vcardField(vcf, "FN"),
			Email:       vcardField(vcf, "EMAIL"),
			Phone:       vcardTypedField(vcf, "TEL", "WORK"), // business telephone
			MobilePhone: vcardTypedField(vcf, "TEL", "CELL"),
			HomePhone:   vcardTypedField(vcf, "TEL", "HOME"),
			Company:     org,
			JobTitle:    vcardField(vcf, "TITLE"),
			Department:  vcardField(vcf, "ROLE"),
			Birthday:    vcardField(vcf, "BDAY"),
			HomeStreet:  adr(homeAdr, 2),
			HomeCity:    adr(homeAdr, 3),
			HomeState:   adr(homeAdr, 4),
			HomePostal:  adr(homeAdr, 5),
			HomeCountry: adr(homeAdr, 6),
			WorkStreet:  adr(workAdr, 2),
			WorkCity:    adr(workAdr, 3),
			WorkState:   adr(workAdr, 4),
			WorkPostal:  adr(workAdr, 5),
			WorkCountry: adr(workAdr, 6),
			IMAddress:   vcardField(vcf, "IMPP"),
			WebPage:     vcardField(vcf, "URL"),
			Assistant:   propString(msg, mapi.PrAssistant),
			Manager:     propString(msg, mapi.PrManagerName),
			Office:      propString(msg, mapi.PrOfficeLocation),
			Anniversary: anniversaryOf(msg),
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
// values are skipped. Anniversary is a PtSysTime (UnixToNTTime uint64).
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
	if len(props) > 0 {
		_ = st.SetMessageProperties(id, props)
	}
}
