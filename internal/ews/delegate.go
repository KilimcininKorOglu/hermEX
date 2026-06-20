package ews

import (
	"encoding/xml"
	"net/http"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// Delegate management (MS-OXWSDLGM). A mailbox's delegate list and the per-folder
// permissions that back it are configured here; the ROP logon path (send-on-behalf
// and per-folder enforcement) reads the same store state. v1 manages only the
// authenticated user's own mailbox: an operation naming another principal's mailbox
// is refused rather than silently acting on the caller's own list.

// --- request types (namespace-agnostic local-name tags, like the package's other
// request parsers, so a client's prefix/namespace choice does not matter) ---

// getDelegateRequest is the GetDelegate operation ([MS-OXWSDLGM] 2.2.4.4): the
// Mailbox names the principal whose delegate list is read, and IncludePermissions
// asks for each delegate's per-folder permission levels.
type getDelegateRequest struct {
	XMLName            xml.Name            `xml:"GetDelegate"`
	IncludePermissions bool                `xml:"IncludePermissions,attr"`
	Mailbox            delegateMailbox     `xml:"Mailbox"`
	UserIds            []delegateReqUserId `xml:"UserIds>UserId"`
}

// delegateMailbox is an EmailAddressType: the principal's SMTP address rides in the
// EmailAddress child (types namespace on the wire, matched namespace-agnostically).
type delegateMailbox struct {
	EmailAddress string `xml:"EmailAddress"`
}

// delegateReqUserId is a request-side UserIdType: only the PrimarySmtpAddress is
// consulted (the identity key the delegate list and folder ACLs are stored under).
type delegateReqUserId struct {
	PrimarySmtpAddress string `xml:"PrimarySmtpAddress"`
}

// containsFold reports whether list holds s under case-insensitive comparison — the
// same case-folded identity the ROP delegate-list check uses.
func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// --- response types (the top element declares the messages namespace; children
// inherit it as the default; t:DelegateUser carries the types namespace, and its
// own children inherit that) ---

// getDelegateResponse is the GetDelegate response ([MS-OXWSDLGM] 2.2.4.4). The
// outer ResponseClass/ResponseCode report the operation result; each
// DelegateUserResponseMessageType reports one delegate's result and payload.
type getDelegateResponse struct {
	XMLName                xml.Name                      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetDelegateResponse"`
	ResponseClass          string                        `xml:"ResponseClass,attr"`
	ResponseCode           string                        `xml:"ResponseCode"`
	Messages               []delegateUserResponseMessage `xml:"ResponseMessages>DelegateUserResponseMessageType"`
	DeliverMeetingRequests string                        `xml:"DeliverMeetingRequests"`
}

type delegateUserResponseMessage struct {
	ResponseClass string        `xml:"ResponseClass,attr"`
	ResponseCode  string        `xml:"ResponseCode"`
	DelegateUser  *delegateUser `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegateUser"`
}

// delegateUser is a DelegateUserType. ReceiveCopiesOfMeetingMessages and
// ViewPrivateItems are always emitted (a strict client expects them present); v1
// does not model meeting-message routing or private-item visibility, so both report
// their conservative default of false. DelegatePermissions is omitted when the
// request set IncludePermissions=false.
type delegateUser struct {
	UserId                         delegateUserId       `xml:"UserId"`
	DelegatePermissions            *delegatePermissions `xml:"DelegatePermissions,omitempty"`
	ReceiveCopiesOfMeetingMessages bool                 `xml:"ReceiveCopiesOfMeetingMessages"`
	ViewPrivateItems               bool                 `xml:"ViewPrivateItems"`
}

type delegateUserId struct {
	PrimarySmtpAddress string `xml:"PrimarySmtpAddress"`
}

// delegatePermissions is a DelegatePermissionsType: the six standard delegate
// folders, each a DelegateFolderPermissionLevelType (None/Reviewer/Author/Editor/Custom).
type delegatePermissions struct {
	CalendarFolderPermissionLevel string `xml:"CalendarFolderPermissionLevel"`
	TasksFolderPermissionLevel    string `xml:"TasksFolderPermissionLevel"`
	InboxFolderPermissionLevel    string `xml:"InboxFolderPermissionLevel"`
	ContactsFolderPermissionLevel string `xml:"ContactsFolderPermissionLevel"`
	NotesFolderPermissionLevel    string `xml:"NotesFolderPermissionLevel"`
	JournalFolderPermissionLevel  string `xml:"JournalFolderPermissionLevel"`
}

// frightsToDelegateLevel maps a folder's frights mask to a
// DelegateFolderPermissionLevelType ([MS-OXWSDLGM] 2.2.5.2). The reverse mapping is
// lossy, so only an EXACT match with a canonical role snaps to a named level; any
// other combination is reported as Custom. Reporting the nearest named role instead
// would silently widen a custom grant on a client's read-modify-write cycle.
//
// The free/busy and contact bits are stripped first: every mailbox seeds a free/busy
// default, so a role grant often carries those ambient bits on top of the role. They
// are orthogonal to the role classification, and clearing them lets a role plus
// free/busy still report as that role rather than collapsing to Custom.
func frightsToDelegateLevel(r uint32) string {
	r &^= mapi.FrightsContact | mapi.FrightsFreeBusySimple | mapi.FrightsFreeBusyDetailed
	switch r {
	case 0:
		return "None"
	case mapi.RightsReviewer:
		return "Reviewer"
	case mapi.RightsAuthor:
		return "Author"
	case mapi.RightsEditor:
		return "Editor"
	default:
		return "Custom"
	}
}

// delegateFolders are the six well-known folders a DelegatePermissions block covers.
var delegateFolders = []int64{
	mapi.PrivateFIDCalendar, mapi.PrivateFIDTasks, mapi.PrivateFIDInbox,
	mapi.PrivateFIDContacts, mapi.PrivateFIDNotes, mapi.PrivateFIDJournal,
}

// folderGrants maps each delegate folder id to that folder's explicit per-user
// grants (lowercased username -> frights). It deliberately excludes the inherited
// "default"/"anonymous" members: a delegate's reported level is their OWN grant,
// never the universal free/busy default that every mailbox seeds.
type folderGrants map[int64]map[string]uint32

// collectFolderGrants reads the explicit per-user grants for every delegate folder.
func collectFolderGrants(st *objectstore.Store) (folderGrants, error) {
	grants := make(folderGrants, len(delegateFolders))
	for _, fid := range delegateFolders {
		entries, err := st.ListPermissions(fid)
		if err != nil {
			return nil, err
		}
		m := make(map[string]uint32, len(entries))
		for _, e := range entries {
			if e.MemberID <= 0 {
				continue // skip default (0) and anonymous (-1); only real members carry a row id
			}
			m[strings.ToLower(e.Name)] = e.Rights
		}
		grants[fid] = m
	}
	return grants, nil
}

// levelsFor reports one delegate's permission level on each of the six folders. The
// lookup is case-folded (lowercased) so a delegate-list entry and its permission
// rows match even when stored in different case — the same case-insensitive identity
// the ROP delegate-list check uses.
func (g folderGrants) levelsFor(delegate string) delegatePermissions {
	key := strings.ToLower(delegate)
	level := func(fid int64) string { return frightsToDelegateLevel(g[fid][key]) }
	return delegatePermissions{
		CalendarFolderPermissionLevel: level(mapi.PrivateFIDCalendar),
		TasksFolderPermissionLevel:    level(mapi.PrivateFIDTasks),
		InboxFolderPermissionLevel:    level(mapi.PrivateFIDInbox),
		ContactsFolderPermissionLevel: level(mapi.PrivateFIDContacts),
		NotesFolderPermissionLevel:    level(mapi.PrivateFIDNotes),
		JournalFolderPermissionLevel:  level(mapi.PrivateFIDJournal),
	}
}

// isOwnMailbox reports whether email names the caller's own mailbox. An absent
// address defaults to self; a present one matches either the authenticated user
// directly or any alias that resolves to the same maildir (mirroring the
// availability owner check).
func (s *Server) isOwnMailbox(sess *session, email string) bool {
	if email == "" || strings.EqualFold(email, sess.user) {
		return true
	}
	path, ok := s.accounts.Resolve(email)
	return ok && path == sess.mailbox
}

// handleGetDelegate answers GetDelegate: it returns the mailbox's delegate list,
// each delegate's per-folder permission levels (when IncludePermissions is set), and
// the meeting-request delivery scope. v1 serves only the caller's own mailbox.
func (s *Server) handleGetDelegate(w http.ResponseWriter, inner []byte, sess *session) {
	var req getDelegateRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetDelegate: "+err.Error())
		return
	}
	if !s.isOwnMailbox(sess, req.Mailbox.EmailAddress) {
		writeSOAPFault(w, "ErrorAccessDenied", "GetDelegate: managing another mailbox's delegates is not supported")
		return
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	delegates, err := st.GetDelegates()
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}

	var grants folderGrants
	if req.IncludePermissions {
		if grants, err = collectFolderGrants(st); err != nil {
			writeSOAPFault(w, "ErrorInternalServerError", err.Error())
			return
		}
	}

	// An optional UserIds filter narrows the result to specific delegates; absent it,
	// every delegate is returned.
	var requested []string
	for _, u := range req.UserIds {
		if u.PrimarySmtpAddress != "" {
			requested = append(requested, u.PrimarySmtpAddress)
		}
	}

	msgs := make([]delegateUserResponseMessage, 0, len(delegates))
	found := make([]string, 0, len(requested))
	for _, d := range delegates {
		if len(requested) > 0 && !containsFold(requested, d) {
			continue
		}
		du := &delegateUser{UserId: delegateUserId{PrimarySmtpAddress: d}}
		if req.IncludePermissions {
			lv := grants.levelsFor(d)
			du.DelegatePermissions = &lv
		}
		msgs = append(msgs, delegateUserResponseMessage{
			ResponseClass: "Success",
			ResponseCode:  "NoError",
			DelegateUser:  du,
		})
		found = append(found, d)
	}
	// A requested delegate that is not on the list is reported per id, mirroring the
	// per-delegate result model.
	for _, r := range requested {
		if !containsFold(found, r) {
			msgs = append(msgs, delegateUserResponseMessage{
				ResponseClass: "Error",
				ResponseCode:  "ErrorDelegateNotFound",
				DelegateUser:  &delegateUser{UserId: delegateUserId{PrimarySmtpAddress: r}},
			})
		}
	}

	writeResponse(w, getDelegateResponse{
		ResponseClass:          "Success",
		ResponseCode:           "NoError",
		Messages:               msgs,
		DeliverMeetingRequests: "DelegatesAndMe", // v1 default; routing scope is not modelled
	})
}
