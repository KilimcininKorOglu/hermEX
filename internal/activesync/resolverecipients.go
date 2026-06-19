package activesync

import (
	"net/http"
	"strconv"

	"hermex/internal/directory"
	"hermex/internal/wbxml"
)

// resolveRecipientLimit caps the GAL matches returned for one recipient query.
const resolveRecipientLimit = 100

// ResolveRecipients Status values (MS-ASCMD 2.2.3.166.2): the overall command
// status and the per-query resolution status carried in each Response.
const (
	rrStatusOK            = 1 // the command succeeded
	rrStatusProtocolError = 5 // the request named no recipient to resolve
	rrResolved            = 1 // one or more recipients matched the query
	rrUnresolved          = 4 // no recipient matched the query
)

// handleResolveRecipients answers ResolveRecipients ([MS-ASCMD] 2.2.2.14): each
// To string is resolved against the directory GAL, and the reply carries one
// Response per query listing its matches (display name + address). v1 resolves
// against the GAL only — certificates, free/busy availability, and pictures are
// not served.
func (s *Server) handleResolveRecipients(w http.ResponseWriter, r *http.Request, _ *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	var tos []string
	for _, c := range root.Children {
		if c.Tag == wbxml.RRTo {
			tos = append(tos, c.Text)
		}
	}
	if len(tos) == 0 {
		writeWBXML(w, wbxml.Elem(wbxml.RRResolveRecipients,
			wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrStatusProtocolError))))
		return
	}

	gal, _ := s.accounts.(directory.GAL)
	children := []*wbxml.Node{wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrStatusOK))}
	for _, to := range tos {
		children = append(children, resolveOneRecipient(gal, to))
	}
	writeWBXML(w, wbxml.Elem(wbxml.RRResolveRecipients, children...))
}

// resolveOneRecipient builds one Response: the echoed query, its resolution
// status, the match count, and a Recipient for each GAL match. A query that
// matches nothing (or a directory with no GAL) is an unresolved Response with a
// zero count, not an error.
func resolveOneRecipient(gal directory.GAL, to string) *wbxml.Node {
	resp := []*wbxml.Node{wbxml.Str(wbxml.RRTo, to)}

	var entries []directory.GALEntry
	if gal != nil && to != "" {
		entries, _ = gal.SearchGAL(to, resolveRecipientLimit)
	}
	if len(entries) == 0 {
		return wbxml.Elem(wbxml.RRResponse, append(resp,
			wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrUnresolved)),
			wbxml.Str(wbxml.RRRecipientCount, "0"))...)
	}

	resp = append(resp,
		wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrResolved)),
		wbxml.Str(wbxml.RRRecipientCount, strconv.Itoa(len(entries))))
	for _, e := range entries {
		resp = append(resp, wbxml.Elem(wbxml.RRRecipient,
			wbxml.Str(wbxml.RRType, "1"), // 1 = a Global Address List entry
			wbxml.Str(wbxml.RRDisplayName, e.DisplayName),
			wbxml.Str(wbxml.RREmailAddress, e.Address)))
	}
	return wbxml.Elem(wbxml.RRResponse, resp...)
}
