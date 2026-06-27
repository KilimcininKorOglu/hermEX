package ews

import (
	"encoding/xml"
	"net/http"
	"strings"

	"hermex/internal/directory"
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

// personaOut is a Persona (types namespace). Only the fields the GAL can fill are
// emitted; the rest are omitted. EmailAddress is a plain address string here, not a
// structured mailbox.
type personaOut struct {
	XMLName             xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Persona"`
	DisplayName         string   `xml:"DisplayName,omitempty"`
	EmailAddress        string   `xml:"EmailAddress,omitempty"`
	Title               string   `xml:"Title,omitempty"`
	Nickname            string   `xml:"Nickname,omitempty"`
	BusinessPhoneNumber string   `xml:"BusinessPhoneNumber,omitempty"`
	MobilePhoneNumber   string   `xml:"MobilePhoneNumber,omitempty"`
	HomeAddress         string   `xml:"HomeAddress,omitempty"`
	Comment             string   `xml:"Comment,omitempty"`
}

type peopleWrap struct {
	Personas []personaOut
}

type findPeopleResponse struct {
	XMLName  xml.Name                    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindPeopleResponse"`
	Messages []findPeopleResponseMessage `xml:"ResponseMessages>FindPeopleResponseMessage"`
}

type findPeopleResponseMessage struct {
	ResponseClass             string      `xml:"ResponseClass,attr"`
	MessageText               string      `xml:"MessageText,omitempty"`
	ResponseCode              string      `xml:"ResponseCode"`
	People                    *peopleWrap `xml:"People,omitempty"`
	TotalNumberOfPeopleInView *int        `xml:"TotalNumberOfPeopleInView,omitempty"`
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
func (s *Server) handleFindPeople(w http.ResponseWriter, inner []byte, _ *session) {
	var req findPeopleRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "FindPeople: "+err.Error())
		return
	}
	var personas []personaOut
	if gal, ok := s.accounts.(directory.GAL); ok {
		entries, err := gal.SearchGAL(req.QueryString, personaSearchLimit)
		if err == nil {
			for _, e := range entries {
				personas = append(personas, personaOut{DisplayName: e.DisplayName, EmailAddress: e.Address})
			}
		}
	}
	msg := findPeopleResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}
	if len(personas) > 0 {
		n := len(personas)
		msg.People = &peopleWrap{Personas: personas}
		msg.TotalNumberOfPeopleInView = &n
	}
	writeResponse(w, findPeopleResponse{Messages: []findPeopleResponseMessage{msg}})
}

// handleGetPersona answers GetPersona: it looks up a single persona by the email
// address in the request. A missing address is ErrorInvalidArgument; an address
// absent from the GAL is ErrorPersonNotFound.
func (s *Server) handleGetPersona(w http.ResponseWriter, inner []byte, _ *session) {
	var req getPersonaRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetPersona: "+err.Error())
		return
	}
	target := strings.TrimSpace(req.EmailAddress.EmailAddress)
	if target == "" {
		writeResponse(w, getPersonaResponse{ResponseClass: "Error", ResponseCode: "ErrorInvalidArgument", MessageText: "EmailAddress is required"})
		return
	}
	var found *personaOut
	if gal, ok := s.accounts.(directory.GAL); ok {
		entries, err := gal.SearchGAL(target, personaSearchLimit)
		if err == nil {
			for _, e := range entries {
				if strings.EqualFold(e.Address, target) {
					found = &personaOut{DisplayName: e.DisplayName, EmailAddress: e.Address}
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
