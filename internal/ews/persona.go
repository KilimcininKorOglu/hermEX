package ews

import (
	"encoding/xml"
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// People and personas (MS-OXWSCOS) expose the address book as aggregated contact
// "personas". hermEX serves them from the directory GAL, matching the reference's
// model: FindPeople resolves a query string against the GAL and returns one persona
// per match, and GetPersona looks a single persona up by its email address. A
// persona is keyed by its address, not by an item id, so there is no PersonaId on
// the wire; the GAL is shared directory data, gated by authentication, not a
// per-mailbox store, so there is no per-mailbox access surface to guard.
//
// hermEX's GAL carries a display name and an address per entry, so those are the
// persona fields it populates; the richer fields (title, phones, nickname) are
// left empty because the directory does not hold them.

const personaSearchLimit = 100

// --- requests ---

type findPeopleRequest struct {
	QueryString string `xml:"QueryString"`
}

type getPersonaRequest struct {
	// The address rides in a Mailbox-shaped EmailAddress wrapper (tEmailAddressType),
	// whose inner EmailAddress element carries the SMTP address.
	EmailAddress struct {
		EmailAddress string `xml:"EmailAddress"`
	} `xml:"EmailAddress"`
}

// --- responses ---

// personaID is the persona's handle. A client keys the row it caches on this, and
// drops a persona that carries none. hermEX has no per-persona store item, so the
// id is the address it was built from, which is stable for the same person.
type personaID struct {
	ID string `xml:"Id,attr"`
}

// personaEmail is the EmailAddressType a persona carries. It is a nested block,
// not an address string: a client reads the address out of the inner element and
// finds nothing when the outer one holds it directly.
type personaEmail struct {
	Name         string `xml:"Name,omitempty"`
	EmailAddress string `xml:"EmailAddress,omitempty"`
	RoutingType  string `xml:"RoutingType,omitempty"`
}

// personaOut is a Persona (types namespace). The element order follows the
// schema's sequence, which is what a validating client checks before it reads
// any of the values.
type personaOut struct {
	XMLName        xml.Name      `xml:"http://schemas.microsoft.com/exchange/services/2006/types Persona"`
	PersonaID      personaID     `xml:"PersonaId"`
	PersonaType    string        `xml:"PersonaType,omitempty"`
	DisplayName    string        `xml:"DisplayName,omitempty"`
	GivenName      string        `xml:"GivenName,omitempty"`
	Surname        string        `xml:"Surname,omitempty"`
	EmailAddress   *personaEmail `xml:"EmailAddress,omitempty"`
	RelevanceScore *int          `xml:"RelevanceScore,omitempty"`
}

type peopleWrap struct {
	Personas []personaOut
}

// findPeopleResponse is the FindPeople answer. FindPeople is NOT a batch
// operation: ResponseClass sits on the root and ResponseCode, People and the row
// counters are its direct children. Wrapping it in the ResponseMessages envelope
// the batch operations use makes a client discard every response whatever it
// holds, which is why autocomplete returned nothing at all.
type findPeopleResponse struct {
	XMLName                   xml.Name    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindPeopleResponse"`
	ResponseClass             string      `xml:"ResponseClass,attr"`
	MessageText               string      `xml:"MessageText,omitempty"`
	ResponseCode              string      `xml:"ResponseCode"`
	People                    *peopleWrap `xml:"People,omitempty"`
	TotalNumberOfPeopleInView *int        `xml:"TotalNumberOfPeopleInView,omitempty"`
	FirstMatchingRowIndex     *int        `xml:"FirstMatchingRowIndex,omitempty"`
	FirstLoadedRowIndex       *int        `xml:"FirstLoadedRowIndex,omitempty"`
}

// getPersonaResponse is the GetPersona response. Its root is the response message
// itself (GetPersona breaks the usual Response/ResponseMessages envelope).
type getPersonaResponse struct {
	XMLName       xml.Name    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetPersonaResponseMessage"`
	ResponseClass string      `xml:"ResponseClass,attr"`
	MessageText   string      `xml:"MessageText,omitempty"`
	ResponseCode  string      `xml:"ResponseCode"`
	Persona       *personaOut `xml:"Persona,omitempty"`
}

// handleFindPeople answers FindPeople: it resolves the query string against the
// directory GAL and returns one persona per match.
func (s *Server) handleFindPeople(w http.ResponseWriter, inner []byte, sess *session) {
	var req findPeopleRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "FindPeople: invalid request", err)
		return
	}
	personas := s.searchPeople(sess, req.QueryString)
	// The counters are always emitted, including the zero case: a client reads
	// them to decide whether to ask for another page, and an absent count is not
	// the same answer as none.
	n, zero := len(personas), 0
	resp := findPeopleResponse{
		ResponseClass: "Success", ResponseCode: "NoError",
		TotalNumberOfPeopleInView: &n,
		FirstMatchingRowIndex:     &zero,
		FirstLoadedRowIndex:       &zero,
	}
	if n > 0 {
		resp.People = &peopleWrap{Personas: personas}
	}
	writeResponse(w, resp)
}

// searchPeople resolves an autocomplete query against the two address sources a
// client expects: the organization's address book and the user's own contacts.
// The contacts matter because an address the user corresponds with is often not
// in the GAL at all, and that is exactly the address autocomplete is for.
//
// The GAL comes first, because a colleague is the likelier match, and an address
// found in both is emitted once.
func (s *Server) searchPeople(sess *session, query string) []personaOut {
	var personas []personaOut
	seen := map[string]bool{}
	add := func(displayName, address string) {
		key := strings.ToLower(strings.TrimSpace(address))
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		personas = append(personas, newPersona(displayName, address))
	}
	if gal, ok := s.accounts.(directory.GAL); ok {
		entries, err := gal.SearchGAL(sess.user, query, personaSearchLimit)
		// Withhold the addresses the operator hid from the address book.
		if err == nil {
			for _, e := range directory.VisibleGAL(entries) {
				add(e.DisplayName, e.Address)
			}
		}
	}
	for _, c := range s.searchOwnContacts(sess, query, personaSearchLimit-len(personas)) {
		add(c.DisplayName, c.Address)
	}
	return personas
}

// searchOwnContacts matches the query against the caller's own Contacts folder.
// The store is the caller's own, so there is no cross-mailbox surface here; a
// mailbox that will not open yields nothing rather than failing the query, since
// the address book half has already answered.
func (s *Server) searchOwnContacts(sess *session, query string, limit int) []objectstore.ContactMatch {
	if limit <= 0 {
		return nil
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		return nil
	}
	defer st.Close()
	matches, err := st.SearchContacts(query, limit)
	if err != nil {
		return nil
	}
	return matches
}

// newPersona builds one persona from a display name and an address. The id is the
// address: hermEX has no per-persona store item, and a client that caches a row
// needs a handle that means the same person on the next query.
func newPersona(displayName, address string) personaOut {
	name := displayName
	if name == "" {
		name = address
	}
	given, surname := splitName(displayName)
	return personaOut{
		PersonaID:   personaID{ID: address},
		PersonaType: "Person",
		DisplayName: name,
		GivenName:   given,
		Surname:     surname,
		EmailAddress: &personaEmail{
			Name:         name,
			EmailAddress: address,
			RoutingType:  "SMTP",
		},
	}
}

// splitName separates a display name into its given name and surname on the
// first space. It yields neither for a name that carries no space, because a
// directory that holds only an address would otherwise report that address as
// somebody's first name, and a client shows an empty field more usefully than a
// wrong one.
func splitName(displayName string) (given, surname string) {
	first, rest, found := strings.Cut(strings.TrimSpace(displayName), " ")
	if !found {
		return "", ""
	}
	return first, strings.TrimSpace(rest)
}

// handleGetPersona answers GetPersona: it looks up a single persona by the email
// address in the request. A missing address is ErrorInvalidArgument; an address
// absent from the GAL is ErrorPersonNotFound.
func (s *Server) handleGetPersona(w http.ResponseWriter, inner []byte, sess *session) {
	var req getPersonaRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "GetPersona: invalid request", err)
		return
	}
	target := strings.TrimSpace(req.EmailAddress.EmailAddress)
	if target == "" {
		writeResponse(w, getPersonaResponse{ResponseClass: "Error", ResponseCode: "ErrorInvalidArgument", MessageText: "EmailAddress is required"})
		return
	}
	var found *personaOut
	if gal, ok := s.accounts.(directory.GAL); ok {
		entries, err := gal.SearchGAL(sess.user, target, personaSearchLimit)
		// A hidden address has no persona to report: it answers ErrorPersonNotFound
		// below, the same as an address absent from the directory.
		entries = directory.VisibleGAL(entries)
		if err == nil {
			for _, e := range entries {
				if strings.EqualFold(e.Address, target) {
					p := newPersona(e.DisplayName, e.Address)
					found = &p
					break
				}
			}
		}
	}
	if found == nil {
		writeResponse(w, getPersonaResponse{ResponseClass: "Error", ResponseCode: "ErrorPersonNotFound", MessageText: "No persona found for the specified email address"})
		return
	}
	writeResponse(w, getPersonaResponse{ResponseClass: "Success", ResponseCode: "NoError", Persona: found})
}
