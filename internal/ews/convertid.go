package ews

import (
	"encoding/xml"
	"net/http"

	"hermex/internal/oxews"
)

// ConvertId (MS-OXWSCDATA) translates item and folder identifiers between the id
// formats Exchange has used across versions: EwsLegacyId, EwsId, EntryId,
// HexEntryId, StoreId, and OwaId.
//
// hermEX only ever mints its own EwsId tokens (oxews.EncodeItemID/EncodeFolderID),
// so the only conversion it can serve honestly is the EwsId-to-EwsId identity: the
// source token is validated by decoding it and echoed back unchanged. A source in
// any other format, or a destination in any other format, is a conversion hermEX
// cannot produce without fabricating a foreign binary id, so it is refused with
// ErrorUnsupportedTypeForConversion rather than returning a fake id. A source that
// claims to be an EwsId but does not decode is ErrorInvalidIdMalformed.
//
// The operation is a pure id transform: it never opens a mailbox store, so there
// is no cross-mailbox access surface to gate. The opaque token already carries its
// own mailbox; echoing it grants no access it did not already encode.

const ewsIdFormat = "EwsId"

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
func (s *Server) handleConvertId(w http.ResponseWriter, inner []byte, sess *session) {
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
func convertOneID(src alternateID, dest string) convertIDMessage {
	if src.Format != ewsIdFormat || dest != ewsIdFormat {
		// hermEX produces and reads only EwsId tokens; any other source or
		// destination format is a conversion it cannot serve.
		return convertIDError("ErrorUnsupportedTypeForConversion")
	}
	if !validEwsID(src.ID) {
		return convertIDError("ErrorInvalidIdMalformed")
	}
	return convertIDMessage{
		ResponseClass: "Success",
		ResponseCode:  "NoError",
		AlternateID:   &alternateIDOut{Format: ewsIdFormat, ID: src.ID, Mailbox: src.Mailbox},
	}
}

// validEwsID reports whether id decodes as one of hermEX's EwsId tokens (an item
// id or a folder id).
func validEwsID(id string) bool {
	if _, err := oxews.DecodeItemID(id); err == nil {
		return true
	}
	if _, _, err := oxews.DecodeFolderID(id); err == nil {
		return true
	}
	return false
}

func convertIDError(code string) convertIDMessage {
	return convertIDMessage{ResponseClass: "Error", ResponseCode: code}
}
