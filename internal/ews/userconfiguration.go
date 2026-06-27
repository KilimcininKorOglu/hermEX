package ews

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"net/http"

	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// User-configuration objects (MS-OXWSUSRCFG) are named blobs a client attaches to
// a folder to roam its own settings (OWA options, the Calendar working hours, a
// add-in's state). Each carries a typed dictionary, an opaque XML section, and an
// opaque binary section, identified by (folder, name).
//
// hermEX persists them as the mailbox's PrUserConfigurations store property — the
// same provider-defined JSON-in-a-property mechanism the out-of-office and sync
// settings use, keyed by the resolving folder id and the name. The dictionary's
// per-entry types and the XML/binary base64 text are stored verbatim, so a config
// round-trips byte-identically. The store property lives only in the caller's own
// mailbox, so every operation resolves its folder through resolveTargets and
// refuses a delegated or public-store target with ErrorAccessDenied.
//
// The ItemId returned for a config is synthesized deterministically from the
// folder and name: store-property storage has no backing message, and clients key
// a config by its name (every Get/Update/Delete request carries UserConfigurationName,
// never the item id), so the id is emitted for wire shape only.

// --- request types (namespace-agnostic local names) ---

type ucName struct {
	Name          string  `xml:"Name,attr"`
	Distinguished []refID `xml:"DistinguishedFolderId"`
	Folders       []refID `xml:"FolderId"`
}

// ucDictObject is a typed dictionary key or value: a Type plus one or more string
// Values (array types carry multiple Value elements). Shared by the request parse
// and the response emit since the shape is identical.
type ucDictObject struct {
	Type   string   `xml:"Type"`
	Values []string `xml:"Value"`
}

type ucDictEntry struct {
	Key   ucDictObject `xml:"DictionaryKey"`
	Value ucDictObject `xml:"DictionaryValue"`
}

type ucConfigIn struct {
	Name       ucName        `xml:"UserConfigurationName"`
	Dictionary []ucDictEntry `xml:"Dictionary>DictionaryEntry"`
	XMLData    string        `xml:"XmlData"`
	BinaryData string        `xml:"BinaryData"`
}

type getUserConfigRequest struct {
	Name       ucName `xml:"UserConfigurationName"`
	Properties string `xml:"UserConfigurationProperties"`
}

type createUserConfigRequest struct {
	Config ucConfigIn `xml:"UserConfiguration"`
}

type updateUserConfigRequest struct {
	Config ucConfigIn `xml:"UserConfiguration"`
}

type deleteUserConfigRequest struct {
	Name ucName `xml:"UserConfigurationName"`
}

// --- response types (root declares the messages namespace; the UserConfiguration
// inherits it; the config's children switch to the types namespace) ---

type ucActionMessage struct {
	ResponseClass string `xml:"ResponseClass,attr"`
	ResponseCode  string `xml:"ResponseCode"`
}

type getUserConfigResponse struct {
	XMLName  xml.Name               `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserConfigurationResponse"`
	Messages []getUserConfigMessage `xml:"ResponseMessages>GetUserConfigurationResponseMessage"`
}

type getUserConfigMessage struct {
	ResponseClass string       `xml:"ResponseClass,attr"`
	ResponseCode  string       `xml:"ResponseCode"`
	Config        *ucConfigOut `xml:"UserConfiguration,omitempty"`
}

type ucConfigOut struct {
	Name       ucNameOut        `xml:"http://schemas.microsoft.com/exchange/services/2006/types UserConfigurationName"`
	ItemID     *ucItemIDOut     `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId,omitempty"`
	Dictionary *ucDictionaryOut `xml:"http://schemas.microsoft.com/exchange/services/2006/types Dictionary,omitempty"`
	XMLData    string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types XmlData,omitempty"`
	BinData    string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types BinaryData,omitempty"`
}

type ucNameOut struct {
	Name          string       `xml:"Name,attr"`
	Distinguished *ucFolderRef `xml:"DistinguishedFolderId,omitempty"`
	FolderID      *ucFolderRef `xml:"FolderId,omitempty"`
}

type ucFolderRef struct {
	ID string `xml:"Id,attr"`
}

type ucItemIDOut struct {
	ID        string `xml:"Id,attr"`
	ChangeKey string `xml:"ChangeKey,attr"`
}

type ucDictionaryOut struct {
	Entries []ucDictEntry `xml:"DictionaryEntry"`
}

type createUserConfigResponse struct {
	XMLName  xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateUserConfigurationResponse"`
	Messages []ucActionMessage `xml:"ResponseMessages>CreateUserConfigurationResponseMessage"`
}

type updateUserConfigResponse struct {
	XMLName  xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateUserConfigurationResponse"`
	Messages []ucActionMessage `xml:"ResponseMessages>UpdateUserConfigurationResponseMessage"`
}

type deleteUserConfigResponse struct {
	XMLName  xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteUserConfigurationResponse"`
	Messages []ucActionMessage `xml:"ResponseMessages>DeleteUserConfigurationResponseMessage"`
}

// handleGetUserConfiguration answers GetUserConfiguration: it returns the named
// config's requested sections, honoring the UserConfigurationProperties selector.
func (s *Server) handleGetUserConfiguration(w http.ResponseWriter, inner []byte, sess *session) {
	var req getUserConfigRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetUserConfiguration: "+err.Error())
		return
	}
	fid, code := resolveConfigFolder(req.Name)
	if code != "" {
		writeResponse(w, getUserConfigResponse{Messages: []getUserConfigMessage{{ResponseClass: "Error", ResponseCode: code}}})
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()
	recs, err := st.GetUserConfigs()
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	idx := indexUserConfig(recs, fid, req.Name.Name)
	if idx < 0 {
		writeResponse(w, getUserConfigResponse{Messages: []getUserConfigMessage{{ResponseClass: "Error", ResponseCode: "ErrorItemNotFound"}}})
		return
	}
	cfg := buildConfigOut(recs[idx], req.Name, req.Properties)
	writeResponse(w, getUserConfigResponse{Messages: []getUserConfigMessage{{ResponseClass: "Success", ResponseCode: "NoError", Config: cfg}}})
}

// handleCreateUserConfiguration answers CreateUserConfiguration: it stores the new
// config, replacing any existing config of the same name in the folder (Exchange
// errors on a duplicate; hermEX accepts the re-create as an upsert).
func (s *Server) handleCreateUserConfiguration(w http.ResponseWriter, inner []byte, sess *session) {
	var req createUserConfigRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "CreateUserConfiguration: "+err.Error())
		return
	}
	fid, code := resolveConfigFolder(req.Config.Name)
	if code != "" {
		writeResponse(w, createUserConfigResponse{Messages: []ucActionMessage{{ResponseClass: "Error", ResponseCode: code}}})
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()
	recs, err := st.GetUserConfigs()
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	rec := configFromRequest(fid, req.Config)
	if idx := indexUserConfig(recs, fid, rec.Name); idx >= 0 {
		recs[idx] = rec
	} else {
		recs = append(recs, rec)
	}
	if err := st.SetUserConfigs(recs); err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	writeResponse(w, createUserConfigResponse{Messages: []ucActionMessage{{ResponseClass: "Success", ResponseCode: "NoError"}}})
}

// handleUpdateUserConfiguration answers UpdateUserConfiguration: it replaces an
// existing config's content with the request's. A config that does not exist is
// ErrorItemNotFound. The EWS Managed API round-trips the whole object, so the
// provided sections replace the stored ones wholesale.
func (s *Server) handleUpdateUserConfiguration(w http.ResponseWriter, inner []byte, sess *session) {
	var req updateUserConfigRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "UpdateUserConfiguration: "+err.Error())
		return
	}
	fid, code := resolveConfigFolder(req.Config.Name)
	if code != "" {
		writeResponse(w, updateUserConfigResponse{Messages: []ucActionMessage{{ResponseClass: "Error", ResponseCode: code}}})
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()
	recs, err := st.GetUserConfigs()
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	idx := indexUserConfig(recs, fid, req.Config.Name.Name)
	if idx < 0 {
		writeResponse(w, updateUserConfigResponse{Messages: []ucActionMessage{{ResponseClass: "Error", ResponseCode: "ErrorItemNotFound"}}})
		return
	}
	recs[idx] = configFromRequest(fid, req.Config)
	if err := st.SetUserConfigs(recs); err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	writeResponse(w, updateUserConfigResponse{Messages: []ucActionMessage{{ResponseClass: "Success", ResponseCode: "NoError"}}})
}

// handleDeleteUserConfiguration answers DeleteUserConfiguration: it removes the
// named config. A config that does not exist is ErrorItemNotFound.
func (s *Server) handleDeleteUserConfiguration(w http.ResponseWriter, inner []byte, sess *session) {
	var req deleteUserConfigRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "DeleteUserConfiguration: "+err.Error())
		return
	}
	fid, code := resolveConfigFolder(req.Name)
	if code != "" {
		writeResponse(w, deleteUserConfigResponse{Messages: []ucActionMessage{{ResponseClass: "Error", ResponseCode: code}}})
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()
	recs, err := st.GetUserConfigs()
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	idx := indexUserConfig(recs, fid, req.Name.Name)
	if idx < 0 {
		writeResponse(w, deleteUserConfigResponse{Messages: []ucActionMessage{{ResponseClass: "Error", ResponseCode: "ErrorItemNotFound"}}})
		return
	}
	recs = append(recs[:idx], recs[idx+1:]...)
	if err := st.SetUserConfigs(recs); err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	writeResponse(w, deleteUserConfigResponse{Messages: []ucActionMessage{{ResponseClass: "Success", ResponseCode: "NoError"}}})
}

// resolveConfigFolder resolves the folder a UserConfigurationName points at to a
// local folder id, enforcing the own-mailbox gate: a delegated or public-store
// target is ErrorAccessDenied, an unmapped or missing folder is ErrorFolderNotFound,
// and a name carrying no folder reference is ErrorInvalidRequest. A non-empty code
// is the per-message error to report.
func resolveConfigFolder(n ucName) (int64, string) {
	targets := resolveTargets(folderRefs{Distinguished: n.Distinguished, Folders: n.Folders})
	if len(targets) == 0 {
		return 0, "ErrorInvalidRequest"
	}
	tgt := targets[0]
	if tgt.mailbox != "" {
		return 0, "ErrorAccessDenied"
	}
	if !tgt.ok {
		if tgt.code != "" {
			return 0, tgt.code
		}
		return 0, "ErrorFolderNotFound"
	}
	return tgt.fid, ""
}

// indexUserConfig returns the index of the (folder, name) config in recs, or -1.
func indexUserConfig(recs []objectstore.UserConfig, fid int64, name string) int {
	for i, r := range recs {
		if r.FID == fid && r.Name == name {
			return i
		}
	}
	return -1
}

// configFromRequest maps a parsed UserConfiguration request body to its stored
// form, keeping the XML/binary base64 text verbatim.
func configFromRequest(fid int64, in ucConfigIn) objectstore.UserConfig {
	return objectstore.UserConfig{
		FID:     fid,
		Name:    in.Name.Name,
		Dict:    toStorageDict(in.Dictionary),
		XMLData: in.XMLData,
		BinData: in.BinaryData,
	}
}

// toStorageDict maps wire dictionary entries to their stored form.
func toStorageDict(entries []ucDictEntry) []objectstore.UserConfigEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]objectstore.UserConfigEntry, len(entries))
	for i, e := range entries {
		out[i] = objectstore.UserConfigEntry{
			KeyType:   e.Key.Type,
			KeyValues: e.Key.Values,
			ValType:   e.Value.Type,
			ValValues: e.Value.Values,
		}
	}
	return out
}

// toWireDict maps stored dictionary entries back to their wire form.
func toWireDict(entries []objectstore.UserConfigEntry) []ucDictEntry {
	out := make([]ucDictEntry, len(entries))
	for i, e := range entries {
		out[i] = ucDictEntry{
			Key:   ucDictObject{Type: e.KeyType, Values: e.KeyValues},
			Value: ucDictObject{Type: e.ValType, Values: e.ValValues},
		}
	}
	return out
}

// buildConfigOut renders a stored config to its wire form, including only the
// sections the UserConfigurationProperties selector requested (an empty or "All"
// selector includes every section; "Id" includes only the name and item id).
func buildConfigOut(rec objectstore.UserConfig, name ucName, props string) *ucConfigOut {
	out := &ucConfigOut{Name: echoConfigName(name), ItemID: synthConfigItemID(rec.FID, rec.Name)}
	all := props == "" || props == "All"
	if (all || props == "Dictionary") && len(rec.Dict) > 0 {
		out.Dictionary = &ucDictionaryOut{Entries: toWireDict(rec.Dict)}
	}
	if all || props == "XmlData" {
		out.XMLData = rec.XMLData
	}
	if all || props == "BinaryData" {
		out.BinData = rec.BinData
	}
	return out
}

// echoConfigName echoes the request's UserConfigurationName (the same folder
// reference shape the client sent) back in the response.
func echoConfigName(n ucName) ucNameOut {
	out := ucNameOut{Name: n.Name}
	if len(n.Distinguished) > 0 {
		out.Distinguished = &ucFolderRef{ID: n.Distinguished[0].ID}
	} else if len(n.Folders) > 0 {
		out.FolderID = &ucFolderRef{ID: n.Folders[0].ID}
	}
	return out
}

// synthConfigItemID derives a deterministic opaque item id for a config from its
// folder and name. Store-property storage has no backing message, so the id is for
// wire shape only; clients address a config by its name, not this id.
func synthConfigItemID(fid int64, name string) *ucItemIDOut {
	h := md5.Sum(fmt.Appendf(nil, "%d/%s", fid, name))
	mid := int64(binary.BigEndian.Uint64(h[:8]) >> 1)
	id := oxews.EncodeItemID(oxews.ItemID{FolderID: fid, MessageID: mid})
	return &ucItemIDOut{ID: id, ChangeKey: oxews.ChangeKey(uint64(mid))}
}
