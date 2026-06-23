package ews

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// getUserPhotoRequest is the MS-OXWSGTRM GetUserPhoto request: the target address
// and (ignored here) the requested size.
type getUserPhotoRequest struct {
	Email         string `xml:"Email"`
	SizeRequested string `xml:"SizeRequested"`
}

// getUserPhotoResponse mirrors the MS-OXWSGTRM response: the response-message
// fields plus HasChanged and the base64 PictureData.
type getUserPhotoResponse struct {
	XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserPhotoResponse"`
	ResponseClass string   `xml:"ResponseClass,attr"`
	ResponseCode  string   `xml:"ResponseCode"`
	HasChanged    bool     `xml:"HasChanged"`
	PictureData   string   `xml:"PictureData,omitempty"` // base64-encoded image bytes
}

// handleGetUserPhoto serves a user's portrait (MS-OXWSGTRM) from the same cross-
// protocol photo property the address book and webmail read, so Outlook shows
// the same picture everywhere.
func (s *Server) handleGetUserPhoto(w http.ResponseWriter, inner []byte, sess *session) {
	var req getUserPhotoRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetUserPhoto: "+err.Error())
		return
	}
	photo := s.userPhoto(req.Email, sess)
	if photo == nil {
		writeResponse(w, getUserPhotoResponse{ResponseClass: "Error", ResponseCode: "ErrorItemNotFound"})
		return
	}
	writeResponse(w, getUserPhotoResponse{
		ResponseClass: "Success",
		ResponseCode:  "NoError",
		HasChanged:    true,
		PictureData:   base64.StdEncoding.EncodeToString(photo),
	})
}

// userPhoto resolves a target address to its mailbox and returns its portrait
// bytes, or nil when none. The caller's own mailbox is served directly; others
// resolve through the GAL (which carries the store path).
func (s *Server) userPhoto(email string, sess *session) []byte {
	if email == "" || strings.EqualFold(email, sess.user) {
		return storePhoto(sess.mailbox)
	}
	gal, ok := s.accounts.(directory.GAL)
	if !ok {
		return nil
	}
	entries, _ := gal.SearchGAL(email, 5)
	for _, e := range entries {
		if strings.EqualFold(e.Address, email) && e.StorePath != "" {
			return storePhoto(e.StorePath)
		}
	}
	return nil
}

// storePhoto opens a mailbox and returns its portrait bytes, or nil.
func storePhoto(path string) []byte {
	if path == "" {
		return nil
	}
	st, err := objectstore.Open(path)
	if err != nil {
		return nil
	}
	defer st.Close()
	p, _ := st.UserPhoto()
	return p
}
