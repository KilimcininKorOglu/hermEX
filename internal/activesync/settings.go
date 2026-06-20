package activesync

import (
	"net/http"
	"strconv"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// easDateDashes is the ActiveSync dashed UTC timestamp (DATE_DASHES) used for OOF
// schedule bounds — the same format the Sync command emits for dates.
const easDateDashes = "2006-01-02T15:04:05.000Z"

// OofState values (MS-ASCMD Settings): disabled, enabled globally, or enabled for
// a scheduled window.
const (
	oofDisabled  = "0"
	oofGlobal    = "1"
	oofTimeBased = "2"
)

// handleSettings answers the MS-ASCMD Settings command. It serves the sub-requests
// a client sends in one Settings document — UserInformation Get (the account's
// addresses), Oof Get/Set (the out-of-office auto-reply), and DeviceInformation /
// DevicePassword Set (acknowledged) — each with its own Status, Get responses
// before Set responses (the order the protocol uses).
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	var getNodes, setNodes []*wbxml.Node
	for _, sub := range root.Children {
		switch sub.Tag {
		case wbxml.STOof:
			if sub.Child(wbxml.STGet) != nil {
				getNodes = append(getNodes, oofGetResponse(st))
			} else if set := sub.Child(wbxml.STSet); set != nil {
				if err := applyOofSet(st, set); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				setNodes = append(setNodes, wbxml.Elem(wbxml.STOof, wbxml.Str(wbxml.STStatus, "1")))
			}
		case wbxml.STUserInformation:
			if sub.Child(wbxml.STGet) != nil {
				getNodes = append(getNodes, userInformationResponse(sess))
			}
		case wbxml.STDeviceInformation:
			if sub.Child(wbxml.STSet) != nil {
				setNodes = append(setNodes, wbxml.Elem(wbxml.STDeviceInformation, wbxml.Str(wbxml.STStatus, "1")))
			}
		case wbxml.STDevicePassword:
			if sub.Child(wbxml.STSet) != nil {
				setNodes = append(setNodes, wbxml.Elem(wbxml.STDevicePassword,
					wbxml.Elem(wbxml.STSet, wbxml.Str(wbxml.STStatus, "1"))))
			}
		}
	}

	resp := make([]*wbxml.Node, 0, len(getNodes)+len(setNodes)+1)
	resp = append(resp, wbxml.Str(wbxml.STStatus, "1"))
	resp = append(resp, getNodes...)
	resp = append(resp, setNodes...)
	writeWBXML(w, wbxml.Elem(wbxml.STSettings, resp...))
}

// oofGetResponse reads the mailbox OOF settings and renders the Oof Get response.
// hermEX's single external reply text is sent to both EAS external buckets, but
// their Enabled bits follow the audience: ExternalKnown is enabled whenever
// external replies are on, while ExternalUnknown is enabled only for the All
// audience — so a known-only configuration reports unknown senders as not replied
// to.
func oofGetResponse(st *objectstore.Store) *wbxml.Node {
	cfg, err := st.GetOOFSettings()
	if err != nil {
		return wbxml.Elem(wbxml.STOof, wbxml.Str(wbxml.STStatus, "2"))
	}
	state := oofDisabled
	if cfg.Enabled {
		if cfg.Start != 0 && cfg.End != 0 {
			state = oofTimeBased
		} else {
			state = oofGlobal
		}
	}
	get := []*wbxml.Node{wbxml.Str(wbxml.STOofState, state)}
	if state == oofTimeBased {
		get = append(get,
			wbxml.Str(wbxml.STStartTime, time.Unix(cfg.Start, 0).UTC().Format(easDateDashes)),
			wbxml.Str(wbxml.STEndTime, time.Unix(cfg.End, 0).UTC().Format(easDateDashes)),
		)
	}
	get = append(get,
		oofMessage(wbxml.STAppliesToInternal, cfg.Enabled, cfg.InternalReply),
		oofMessage(wbxml.STAppliesToExternalKnown, cfg.ExternalEnabled, cfg.ExternalReply),
		oofMessage(wbxml.STAppliesToExternalUnknown,
			cfg.ExternalEnabled && cfg.ExternalAudience == objectstore.OOFExternalAll, cfg.ExternalReply),
	)
	return wbxml.Elem(wbxml.STOof,
		wbxml.Str(wbxml.STStatus, "1"),
		wbxml.Elem(wbxml.STGet, get...),
	)
}

// oofMessage builds one OofMessage block: an AppliesTo discriminator plus the
// reply state.
func oofMessage(appliesTo wbxml.Tag, enabled bool, reply string) *wbxml.Node {
	return wbxml.Elem(wbxml.STOofMessage,
		wbxml.Empty(appliesTo),
		wbxml.Str(wbxml.STEnabled, boolBit(enabled)),
		wbxml.Str(wbxml.STReplyMessage, reply),
		wbxml.Str(wbxml.STBodyType, "Text"),
	)
}

// applyOofSet writes an Oof Set into the mailbox OOF settings. It read-merges so a
// field the EAS wire does not carry — the per-audience subjects set via webmail or
// the admin UI — survives. The two EAS external buckets map onto hermEX's single
// external reply plus an audience selector: ExternalUnknown enabled is the All
// audience, only ExternalKnown enabled is the Known (contacts-only) audience, and
// neither enabled turns external replies off. The reply text comes from the
// ExternalKnown bucket a client always sends, falling back to ExternalUnknown.
func applyOofSet(st *objectstore.Store, set *wbxml.Node) error {
	cfg, err := st.GetOOFSettings()
	if err != nil {
		return err
	}
	switch set.ChildText(wbxml.STOofState) {
	case oofDisabled:
		cfg.Enabled = false
	case oofGlobal:
		cfg.Enabled = true
		cfg.Start, cfg.End = 0, 0
	case oofTimeBased:
		cfg.Enabled = true
		cfg.Start = parseEASTime(set.ChildText(wbxml.STStartTime))
		cfg.End = parseEASTime(set.ChildText(wbxml.STEndTime))
	}

	var sawKnown, knownEnabled, sawUnknown, unknownEnabled bool
	var knownReply, unknownReply string
	for _, m := range set.Children {
		if m.Tag != wbxml.STOofMessage {
			continue
		}
		enabled := m.ChildText(wbxml.STEnabled) == "1"
		reply := m.ChildText(wbxml.STReplyMessage)
		switch {
		case m.Child(wbxml.STAppliesToInternal) != nil:
			cfg.InternalReply = reply
		case m.Child(wbxml.STAppliesToExternalKnown) != nil:
			sawKnown, knownEnabled, knownReply = true, enabled, reply
		case m.Child(wbxml.STAppliesToExternalUnknown) != nil:
			sawUnknown, unknownEnabled, unknownReply = true, enabled, reply
		}
	}
	if sawKnown || sawUnknown {
		switch {
		case unknownEnabled:
			cfg.ExternalEnabled, cfg.ExternalAudience = true, objectstore.OOFExternalAll
		case knownEnabled:
			cfg.ExternalEnabled, cfg.ExternalAudience = true, objectstore.OOFExternalKnown
		default:
			cfg.ExternalEnabled = false
		}
		switch {
		case sawKnown && knownReply != "":
			cfg.ExternalReply = knownReply
		case sawUnknown && unknownReply != "":
			cfg.ExternalReply = unknownReply
		}
	}
	return st.SetOOFSettings(cfg)
}

// userInformationResponse renders the account's addresses in the shape the
// negotiated protocol version expects: 14.1+ nests EmailAddresses under
// Accounts > Account, while 12.0–14.0 places EmailAddresses directly under Get.
// The wrong shape leaves a client unable to read its own address.
func userInformationResponse(sess *session) *wbxml.Node {
	addrs := wbxml.Elem(wbxml.STEmailAddresses, wbxml.Str(wbxml.STSmtpAddress, sess.user))
	var get *wbxml.Node
	if protoAtLeast(sess.protocol, 14.1) {
		get = wbxml.Elem(wbxml.STGet,
			wbxml.Elem(wbxml.STAccounts, wbxml.Elem(wbxml.STAccount, addrs)))
	} else {
		get = wbxml.Elem(wbxml.STGet, addrs)
	}
	return wbxml.Elem(wbxml.STUserInformation, wbxml.Str(wbxml.STStatus, "1"), get)
}

// parseEASTime parses an ActiveSync dashed UTC timestamp to unix seconds, tolerating
// the milliseconds-omitted form some clients send; an unparseable value is 0
// (unbounded).
func parseEASTime(s string) int64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(easDateDashes, s); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z", s); err == nil {
		return t.Unix()
	}
	return 0
}

// protoAtLeast reports whether the negotiated protocol version string is at least
// min (e.g. 14.1).
func protoAtLeast(v string, min float64) bool {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return false
	}
	return f >= min
}

// boolBit renders a boolean as the "1"/"0" EAS uses for flag elements.
func boolBit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
