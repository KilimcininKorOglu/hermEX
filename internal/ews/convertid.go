package ews

import (
	"encoding/xml"
	"net/http"
)

// ConvertId (MS-OXWSCDATA) translates item and folder identifiers between the id
// formats Exchange has used across versions: EwsLegacyId, EwsId, EntryId,
// HexEntryId, StoreId, and OwaId.
//
// A same-format conversion is a passthrough: the source id is echoed back
// unchanged. A cross-format conversion would require re-encoding the id into a
// different id scheme; hermEX's EwsId tokens are its own opaque coordinates, not
// the binary entry ids the other formats encode, so it cannot produce them and
// refuses the conversion with ErrorUnsupportedTypeForConversion rather than
// returning a fabricated id.
//
// The operation is a pure id transform: it never opens a mailbox store, so there
// is no cross-mailbox access surface to gate. The opaque token already carries its
// own mailbox; echoing it grants no access it did not already encode.

type alternateID struct {
	Format    string `xml:"Format,attr"`
	ID        string `xml:"Id,attr"`
	Mailbox   string `xml:"Mailbox,attr"`
	IsArchive bool   `xml:"IsArchive,attr"`
}

type convertIDRequest struct {
	DestinationFormat string        `xml:"DestinationFormat,attr"`
	SourceIDs         []alternateID `xml:"SourceIds>AlternateId"`
}

type convertIDResponse struct {
	XMLName  xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ConvertIdResponse"`
	Messages []convertIDMessage `xml:"ResponseMessages>ConvertIdResponseMessage"`
}

type convertIDMessage struct {
	ResponseClass string          `xml:"ResponseClass,attr"`
	ResponseCode  string          `xml:"ResponseCode"`
	AlternateID   *alternateIDOut `xml:"AlternateId,omitempty"`
}

// alternateIDOut is the converted id in a response. It inherits the messages
// namespace from the response root, matching the format Exchange emits.
type alternateIDOut struct {
	Format  string `xml:"Format,attr"`
	ID      string `xml:"Id,attr"`
	Mailbox string `xml:"Mailbox,attr"`
}

// handleConvertId answers ConvertId, converting each source id to the requested
// destination format (only the EwsId-to-EwsId identity is supported).
func (s *Server) handleConvertId(w http.ResponseWriter, inner []byte, _ *session) {
	var req convertIDRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "ConvertId: "+err.Error())
		return
	}
	msgs := make([]convertIDMessage, 0, len(req.SourceIDs))
	for _, src := range req.SourceIDs {
		msgs = append(msgs, convertOneID(src, req.DestinationFormat))
	}
	writeResponse(w, convertIDResponse{Messages: msgs})
}

// convertOneID converts a single source id, returning the per-id response message.
// A same-format request echoes the id; a cross-format request is unsupported.
func convertOneID(src alternateID, dest string) convertIDMessage {
	if src.Format == dest {
		return convertIDMessage{
			ResponseClass: "Success",
			ResponseCode:  "NoError",
			AlternateID:   &alternateIDOut{Format: dest, ID: src.ID, Mailbox: src.Mailbox},
		}
	}
	return convertIDError("ErrorUnsupportedTypeForConversion")
}

func convertIDError(code string) convertIDMessage {
	return convertIDMessage{ResponseClass: "Error", ResponseCode: code}
}
