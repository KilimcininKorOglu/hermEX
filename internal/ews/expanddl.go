package ews

import (
	"encoding/xml"
	"net/http"
	"strings"

	"hermex/internal/directory"
)

// Distribution-list expansion (MS-OXWSDLMEM ExpandDL) lists a public distribution
// list's direct members. hermEX expands the list through the directory's
// ExpandMList, passing the list address as the sender so the list's posting
// privilege is bypassed: ExpandDL is a directory read of the membership, not a
// post, so a posting restriction must not hide the members. Recursive expansion
// is not supported (a member that is itself a list is returned verbatim), matching
// the operation's one-level contract.

// mlistExpander is the directory capability of expanding a distribution-list
// address to its direct members. A directory that cannot expand (e.g. a static
// test directory) makes ExpandDL report no resolution rather than fault.
type mlistExpander interface {
	ExpandMList(listAddr, from string) ([]string, directory.MListResult, error)
}

// --- request wire types ---

type expandDLRequest struct {
	Mailbox struct {
		EmailAddress string `xml:"EmailAddress"`
	} `xml:"Mailbox"`
}

// --- response wire types ---

type expandDLResponse struct {
	XMLName          xml.Name                 `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ExpandDLResponse"`
	ResponseMessages expandDLResponseMessages `xml:"ResponseMessages"`
}

type expandDLResponseMessages struct {
	Message expandDLResponseMessage `xml:"ExpandDLResponseMessage"`
}

type expandDLResponseMessage struct {
	ResponseClass string       `xml:"ResponseClass,attr"`
	ResponseCode  string       `xml:"ResponseCode"`
	DLExpansion   *dlExpansion `xml:"DLExpansion,omitempty"`
}

// dlExpansion carries the member list. TotalItemsInView is the count and
// IncludesLastItemInRange is always true (hermEX returns the whole membership in
// one call; ExpandDL has no paging in v1).
type dlExpansion struct {
	TotalItemsInView        int        `xml:"TotalItemsInView,attr"`
	IncludesLastItemInRange bool       `xml:"IncludesLastItemInRange,attr"`
	Mailboxes               []dlMember `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
}

type dlMember struct {
	Name         string `xml:"Name"`
	EmailAddress string `xml:"EmailAddress"`
	RoutingType  string `xml:"RoutingType"`
	MailboxType  string `xml:"MailboxType"`
}

// --- handler ---

// handleExpandDL answers ExpandDL: it resolves the named public distribution list
// to its direct members and returns them. An address that is not a distribution
// list, or a directory that cannot expand, yields ErrorNameResolutionNoResults.
func (s *Server) handleExpandDL(w http.ResponseWriter, inner []byte, _ *session) {
	var req expandDLRequest
	_ = xml.Unmarshal(inner, &req)
	addr := strings.TrimSpace(req.Mailbox.EmailAddress)

	exp, ok := s.accounts.(mlistExpander)
	if !ok || addr == "" {
		writeResponse(w, expandDLError("ErrorNameResolutionNoResults"))
		return
	}

	members, result, err := exp.ExpandMList(addr, addr)
	if err != nil {
		writeResponse(w, expandDLError("ErrorInternalServerError"))
		return
	}
	if result != directory.MListOK {
		writeResponse(w, expandDLError("ErrorNameResolutionNoResults"))
		return
	}

	out := make([]dlMember, 0, len(members))
	for _, m := range members {
		out = append(out, dlMember{Name: m, EmailAddress: m, RoutingType: "SMTP", MailboxType: "Mailbox"})
	}
	writeResponse(w, expandDLResponse{
		ResponseMessages: expandDLResponseMessages{
			Message: expandDLResponseMessage{
				ResponseClass: "Success",
				ResponseCode:  "NoError",
				DLExpansion: &dlExpansion{
					TotalItemsInView:        len(out),
					IncludesLastItemInRange: true,
					Mailboxes:               out,
				},
			},
		},
	})
}

// expandDLError builds an ExpandDL error response carrying the given code.
func expandDLError(code string) expandDLResponse {
	return expandDLResponse{
		ResponseMessages: expandDLResponseMessages{
			Message: expandDLResponseMessage{ResponseClass: "Error", ResponseCode: code},
		},
	}
}
