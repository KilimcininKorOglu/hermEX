package ews

import (
	"encoding/xml"
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// Out-of-office settings (MS-OXWSOOF GetUserOofSettings / SetUserOofSettings) over
// hermEX's stored OOFSettings. The wire UserOofSettings carries the state, the
// external audience, the optional scheduled window, and the internal/external
// reply bodies; it does NOT carry a reply subject, so a Set reads-modifies-writes
// the stored settings and preserves the subjects an admin or webmail set.
//
// The operation names a target Mailbox. OOF is served only for the caller's own
// mailbox (there is no OOF delegation in v1): the target address is resolved and
// compared to the authenticated mailbox, so an alias or a login that differs from
// the primary SMTP still matches, and a foreign mailbox is denied (OWASP A01).

// EWS OofState values (MS-OXWSOOF 2.2.5.2 OofState): off, on indefinitely, or on
// for a scheduled window.
const (
	oofStateDisabled  = "Disabled"
	oofStateEnabled   = "Enabled"
	oofStateScheduled = "Scheduled"
)

// EWS ExternalAudience values (MS-OXWSOOF 2.2.5.1): no external reply, only
// senders in Contacts, or every external sender.
const (
	oofAudienceNone  = "None"
	oofAudienceKnown = "Known"
	oofAudienceAll   = "All"
)

// oofTimeLayout is the EWS Duration wire form: an xs:dateTime in UTC. The OOF
// duration is always UTC (the spec examples carry no zone suffix), so it is
// parsed and emitted as UTC, never the server's local zone.
const oofTimeLayout = "2006-01-02T15:04:05"

// --- request wire types ---

// oofMailbox is the request's target Mailbox (types namespace); only Address is
// load-bearing for the identity gate.
type oofMailbox struct {
	Address string `xml:"Address"`
}

type getUserOofSettingsRequest struct {
	Mailbox oofMailbox `xml:"Mailbox"`
}

type setUserOofSettingsRequest struct {
	Mailbox  oofMailbox      `xml:"Mailbox"`
	Settings wireOofSettings `xml:"UserOofSettings"`
}

// wireOofSettings is the UserOofSettings the client sends (and the OofSettings the
// server returns share the same shape).
type wireOofSettings struct {
	OofState         string       `xml:"OofState"`
	ExternalAudience string       `xml:"ExternalAudience"`
	Duration         *oofDuration `xml:"Duration"`
	InternalReply    oofReply     `xml:"InternalReply"`
	ExternalReply    oofReply     `xml:"ExternalReply"`
}

type oofDuration struct {
	StartTime string `xml:"StartTime"`
	EndTime   string `xml:"EndTime"`
}

type oofReply struct {
	Message string `xml:"Message"`
}

// --- response wire types ---

// oofResponseMessage is the nested ResponseMessage element OOF carries (unlike
// most operations, the response class is not an attribute of the response root).
type oofResponseMessage struct {
	ResponseClass string `xml:"ResponseClass,attr"`
	ResponseCode  string `xml:"ResponseCode"`
}

type getUserOofSettingsResponse struct {
	XMLName          xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserOofSettingsResponse"`
	ResponseMessage  oofResponseMessage `xml:"ResponseMessage"`
	OofSettings      *wireOofSettings   `xml:"http://schemas.microsoft.com/exchange/services/2006/types OofSettings,omitempty"`
	AllowExternalOof string             `xml:"AllowExternalOof,omitempty"`
}

type setUserOofSettingsResponse struct {
	XMLName         xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SetUserOofSettingsResponse"`
	ResponseMessage oofResponseMessage `xml:"ResponseMessage"`
}

// --- handlers ---

// handleGetUserOofSettings answers GetUserOofSettings: it returns the caller's
// stored out-of-office configuration mapped to the wire OofSettings, gated so only
// the caller's own mailbox is read.
func (s *Server) handleGetUserOofSettings(w http.ResponseWriter, inner []byte, sess *session) {
	var req getUserOofSettingsRequest
	_ = xml.Unmarshal(inner, &req)

	if !s.oofTargetIsSelf(req.Mailbox.Address, sess) {
		writeResponse(w, getUserOofSettingsResponse{
			ResponseMessage: oofResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorAccessDenied"},
		})
		return
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeResponse(w, getUserOofSettingsResponse{
			ResponseMessage: oofResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"},
		})
		return
	}
	defer st.Close()

	cfg, err := st.GetOOFSettings()
	if err != nil {
		writeResponse(w, getUserOofSettingsResponse{
			ResponseMessage: oofResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"},
		})
		return
	}

	writeResponse(w, getUserOofSettingsResponse{
		ResponseMessage:  oofResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"},
		OofSettings:      oofToWire(cfg),
		AllowExternalOof: oofAudienceAll, // hermEX places no org-policy ceiling on external OOF
	})
}

// handleSetUserOofSettings answers SetUserOofSettings: it overlays the wire
// settings onto the caller's stored configuration (preserving the reply subjects
// the wire does not carry) and writes them back, gated to the caller's own
// mailbox.
func (s *Server) handleSetUserOofSettings(w http.ResponseWriter, inner []byte, sess *session) {
	var req setUserOofSettingsRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeResponse(w, setOofError("ErrorInvalidRequest"))
		return
	}

	if !s.oofTargetIsSelf(req.Mailbox.Address, sess) {
		writeResponse(w, setOofError("ErrorAccessDenied"))
		return
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeResponse(w, setOofError("ErrorInternalServerError"))
		return
	}
	defer st.Close()

	cfg, err := st.GetOOFSettings()
	if err != nil {
		writeResponse(w, setOofError("ErrorInternalServerError"))
		return
	}
	applyWireOof(&cfg, req.Settings)
	if err := st.SetOOFSettings(cfg); err != nil {
		writeResponse(w, setOofError("ErrorInternalServerError"))
		return
	}

	writeResponse(w, setUserOofSettingsResponse{
		ResponseMessage: oofResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"},
	})
}

// setOofError builds a SetUserOofSettings error response carrying the given code.
func setOofError(code string) setUserOofSettingsResponse {
	return setUserOofSettingsResponse{
		ResponseMessage: oofResponseMessage{ResponseClass: "Error", ResponseCode: code},
	}
}

// oofTargetIsSelf reports whether an OOF request's target Mailbox is the
// authenticated caller's own. The address is resolved to a mailbox path and
// compared to the authenticated path, so an alias or a login that differs from
// the primary SMTP still matches; an empty address means the caller's own
// mailbox. A foreign or unresolvable address is not self (denied uniformly so the
// response cannot be used to probe which addresses exist).
func (s *Server) oofTargetIsSelf(address string, sess *session) bool {
	address = strings.TrimSpace(address)
	if address == "" {
		return true
	}
	path, ok := s.accounts.Resolve(address)
	return ok && path == sess.mailbox
}

// --- mapping ---

// oofToWire maps the stored OOFSettings to the wire OofSettings. The OofState is
// Disabled when off, Scheduled when a complete window is set, else Enabled. The
// Duration is always emitted (the EWS Managed API constructs it unconditionally);
// for an unscheduled config it carries a concrete, client-ignored window.
func oofToWire(cfg objectstore.OOFSettings) *wireOofSettings {
	state := oofStateDisabled
	if cfg.Enabled {
		if cfg.Start != 0 && cfg.End != 0 {
			state = oofStateScheduled
		} else {
			state = oofStateEnabled
		}
	}

	audience := oofAudienceNone
	if cfg.ExternalEnabled {
		if cfg.ExternalAudience == objectstore.OOFExternalKnown {
			audience = oofAudienceKnown
		} else {
			audience = oofAudienceAll
		}
	}

	start, end := cfg.Start, cfg.End
	if end <= start {
		// An unset or open-ended window still needs a valid, non-degenerate
		// Duration; the client ignores it unless the state is Scheduled.
		end = start + int64((24 * time.Hour).Seconds())
	}

	return &wireOofSettings{
		OofState:         state,
		ExternalAudience: audience,
		Duration: &oofDuration{
			StartTime: oofFormatTime(start),
			EndTime:   oofFormatTime(end),
		},
		InternalReply: oofReply{Message: cfg.InternalReply},
		ExternalReply: oofReply{Message: cfg.ExternalReply},
	}
}

// applyWireOof overlays the wire settings onto the stored configuration. Only the
// fields the wire carries are touched; the reply subjects are preserved, so a Set
// from a client that has no subject field does not wipe an admin-set subject.
func applyWireOof(cfg *objectstore.OOFSettings, s wireOofSettings) {
	switch s.OofState {
	case oofStateDisabled:
		cfg.Enabled = false
	case oofStateEnabled:
		cfg.Enabled = true
		cfg.Start, cfg.End = 0, 0
	case oofStateScheduled:
		cfg.Enabled = true
		if s.Duration != nil {
			cfg.Start = oofParseTime(s.Duration.StartTime)
			cfg.End = oofParseTime(s.Duration.EndTime)
		}
	}

	switch s.ExternalAudience {
	case oofAudienceNone:
		cfg.ExternalEnabled = false
	case oofAudienceKnown:
		cfg.ExternalEnabled = true
		cfg.ExternalAudience = objectstore.OOFExternalKnown
	case oofAudienceAll:
		cfg.ExternalEnabled = true
		cfg.ExternalAudience = objectstore.OOFExternalAll
	}

	cfg.InternalReply = s.InternalReply.Message
	cfg.ExternalReply = s.ExternalReply.Message
}

// oofFormatTime renders a unix time as the EWS Duration wire form (UTC).
func oofFormatTime(sec int64) string {
	return time.Unix(sec, 0).UTC().Format(oofTimeLayout)
}

// oofParseTime parses an EWS Duration time to unix seconds, accepting both the
// zoneless UTC form and an RFC 3339 timestamp with an explicit zone. An empty or
// unparseable value is 0 (an unset bound).
func oofParseTime(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339, oofTimeLayout} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC().Unix()
		}
	}
	return 0
}
