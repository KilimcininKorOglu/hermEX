package activesync

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
	"sync/atomic"
)

// resolveRecipientLimit caps the GAL matches returned for one recipient query.
const resolveRecipientLimit = 100

// ResolveRecipients Status values (MS-ASCMD 2.2.3.166.2): the overall command
// status, the per-query resolution status, and the per-picture status.
const (
	rrStatusOK            = 1 // the command succeeded
	rrStatusProtocolError = 5 // the request named no recipient to resolve
	rrResolved            = 1 // one or more recipients matched the query
	rrUnresolved          = 4 // no recipient matched the query

	rrPictureOK       = 1   // a portrait is returned
	rrPictureNone     = 173 // the recipient has no portrait
	rrPictureTooLarge = 174 // the portrait exceeds the requested MaxSize
	rrPictureLimit    = 175 // the requested MaxPictures cap was reached
)

// pictureOpts is the client's Options>Picture request: whether portraits are
// wanted and the optional size/count caps.
type pictureOpts struct {
	want        bool
	maxSize     int
	maxPictures int
}

// handleResolveRecipients answers ResolveRecipients ([MS-ASCMD] 2.2.2.14): each
// To string is resolved against the directory GAL, and the reply carries one
// Response per query listing its matches (display name + address, the free/busy
// data when Options>Availability asked for it, and the portrait when
// Options>Picture did).
func (s *Server) handleResolveRecipients(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		s.failRequest(w, r, "wbxml.parse.fail", err, http.StatusBadRequest, "invalid WBXML")
		return
	}
	var tos []string
	var opt pictureOpts
	var win availabilityWindow
	for _, c := range root.Children {
		switch c.Tag {
		case wbxml.RRTo:
			tos = append(tos, c.Text)
		case wbxml.RROptions:
			opt = parsePictureOpts(c)
			win = parseAvailability(c)
		}
	}
	if len(tos) == 0 {
		writeWBXML(w, wbxml.Elem(wbxml.RRResolveRecipients,
			wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrStatusProtocolError))))
		return
	}

	gal, _ := s.accounts.(directory.GAL)
	children := []*wbxml.Node{wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrStatusOK))}
	// Each entry can resolve to many mailboxes and, with availability requested,
	// each match opens a store and scans a calendar, so bound the entry count.
	if max := maxFreeBusyTargets(); len(tos) > max {
		tos = tos[:max]
	}
	pictures := 0
	for _, to := range tos {
		children = append(children, resolveOneRecipient(gal, to, opt, win, sess, &pictures))
	}
	writeWBXML(w, wbxml.Elem(wbxml.RRResolveRecipients, children...))
}

// parsePictureOpts reads the Options>Picture request: its presence means
// portraits are wanted, with optional MaxSize/MaxPictures caps.
func parsePictureOpts(options *wbxml.Node) pictureOpts {
	var o pictureOpts
	for _, c := range options.Children {
		if c.Tag != wbxml.RRPicture {
			continue
		}
		o.want = true
		for _, p := range c.Children {
			switch p.Tag {
			case wbxml.RRMaxSize:
				o.maxSize, _ = strconv.Atoi(p.Text)
			case wbxml.RRMaxPictures:
				o.maxPictures, _ = strconv.Atoi(p.Text)
			}
		}
	}
	return o
}

// resolvedRecipient is one match for a query, and where it came from. A contact
// is one of the caller's own saved contacts: it has no mailbox of its own, so it
// carries an empty StorePath and its picture comes from the caller's own store.
type resolvedRecipient struct {
	entry   directory.GALEntry
	contact bool
}

// resolveOneRecipient builds one Response: the echoed query, its resolution
// status, the match count, and a Recipient for each match (with its free/busy and
// portrait when requested). A query that matches nothing is an unresolved Response
// with a zero count, not an error.
func resolveOneRecipient(gal directory.GAL, to string, opt pictureOpts, win availabilityWindow, sess *session, pictures *int) *wbxml.Node {
	resp := []*wbxml.Node{wbxml.Str(wbxml.RRTo, to)}

	matches := resolveEntries(gal, to, sess)
	if len(matches) == 0 {
		return wbxml.Elem(wbxml.RRResponse, append(resp,
			wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrUnresolved)),
			wbxml.Str(wbxml.RRRecipientCount, "0"))...)
	}

	resp = append(resp,
		wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrResolved)),
		wbxml.Str(wbxml.RRRecipientCount, strconv.Itoa(len(matches))))
	for _, m := range matches {
		resp = append(resp, recipientNode(m, opt, win, sess, pictures))
	}
	return wbxml.Elem(wbxml.RRResponse, resp...)
}

// resolveEntries matches a query against the two address sources a client
// expects: the organization's address book, then the caller's own contacts. The
// address book comes first, because a colleague is the likelier match, and an
// address in both is reported once.
func resolveEntries(gal directory.GAL, to string, sess *session) []resolvedRecipient {
	if to == "" {
		return nil
	}
	var matches []resolvedRecipient
	seen := map[string]bool{}
	add := func(e directory.GALEntry, contact bool) {
		key := strings.ToLower(strings.TrimSpace(e.Address))
		if key == "" || seen[key] || len(matches) >= resolveRecipientLimit {
			return
		}
		seen[key] = true
		matches = append(matches, resolvedRecipient{entry: e, contact: contact})
	}
	if gal != nil {
		entries, _ := gal.SearchGAL(sess.user, to, resolveRecipientLimit)
		// Withhold the addresses the operator hid from name resolution.
		for _, e := range directory.ResolvableGAL(entries) {
			add(e, false)
		}
	}
	for _, c := range ownContacts(sess.mailbox, to, resolveRecipientLimit-len(matches)) {
		add(directory.GALEntry{DisplayName: c.DisplayName, Address: c.Address}, true)
	}
	return matches
}

// ownContacts matches a query against the caller's own Contacts folder. A mailbox
// that will not open yields nothing rather than failing the query, because the
// address-book half has already answered. A query shorter than three characters
// matches nothing (SearchContacts holds the address book back while the user is
// still typing).
func ownContacts(mailbox, query string, limit int) []objectstore.ContactMatch {
	if mailbox == "" || limit <= 0 {
		return nil
	}
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil
	}
	defer st.Close()
	matches, err := st.SearchContacts(query, limit)
	if err != nil {
		return nil
	}
	return matches
}

// recipientNode builds one Recipient: its type, name and address, plus the
// free/busy and picture the client asked for.
func recipientNode(m resolvedRecipient, opt pictureOpts, win availabilityWindow, sess *session, pictures *int) *wbxml.Node {
	// [MS-ASCMD] 2.2.3.186.5: Type is 1 for a GAL entry and 2 for a contact entry.
	kind := "1"
	if m.contact {
		kind = "2"
	}
	rc := []*wbxml.Node{
		wbxml.Str(wbxml.RRType, kind),
		wbxml.Str(wbxml.RRDisplayName, m.entry.DisplayName),
		wbxml.Str(wbxml.RREmailAddress, m.entry.Address),
	}
	if win.ok {
		// A contact carries no StorePath, so availabilityNode reports no data for
		// it, which is the truth: a saved contact has no calendar here to read.
		rc = append(rc, availabilityNode(m.entry, win, sess))
	}
	if opt.want {
		rc = append(rc, pictureNode(m, opt, sess, pictures))
	}
	return wbxml.Elem(wbxml.RRRecipient, rc...)
}

// pictureNode builds a recipient's Picture element, serving the portrait from the
// cross-protocol photo property (or, for one of the caller's own contacts, from
// that contact's card) and honoring the MaxSize/MaxPictures caps.
func pictureNode(m resolvedRecipient, opt pictureOpts, sess *session, pictures *int) *wbxml.Node {
	if opt.maxPictures > 0 && *pictures >= opt.maxPictures {
		return wbxml.Elem(wbxml.RRPicture, wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrPictureLimit)))
	}
	photo := recipientPhoto(m.entry.StorePath)
	if m.contact {
		photo = contactPhoto(sess.mailbox, m.entry.Address)
	}
	if photo == nil {
		return wbxml.Elem(wbxml.RRPicture, wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrPictureNone)))
	}
	if opt.maxSize > 0 && len(photo) > opt.maxSize {
		return wbxml.Elem(wbxml.RRPicture, wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrPictureTooLarge)))
	}
	*pictures++
	return wbxml.Elem(wbxml.RRPicture,
		wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrPictureOK)),
		wbxml.Str(wbxml.RRData, base64.StdEncoding.EncodeToString(photo)))
}

// recipientPhoto opens a mailbox and returns its portrait bytes, or nil.
func recipientPhoto(storePath string) []byte {
	if storePath == "" {
		return nil
	}
	st, err := objectstore.Open(storePath)
	if err != nil {
		return nil
	}
	defer st.Close()
	p, _ := st.UserPhoto()
	return p
}

// contactPhoto returns the picture the caller saved on their own contact card for
// an address, or nil. The store read is the caller's own mailbox.
func contactPhoto(mailbox, address string) []byte {
	if mailbox == "" {
		return nil
	}
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil
	}
	defer st.Close()
	p, _ := st.ContactPhotoFor(address)
	return p
}

// defaultFreeBusyTargets caps how many mailboxes one availability request may fan
// out to when no operator limit is set. Each target costs a store open and a full
// calendar scan, and the request admits far more list entries than that, so the
// count needs its own bound; the byte cap does not provide one.
const defaultFreeBusyTargets = 100

// freeBusyTargetLimit holds the operator-set availability target cap (0 = use the
// default), set by SetMaxFreeBusyTargets and read live per request.
var freeBusyTargetLimit atomic.Int64

// SetMaxFreeBusyTargets sets how many mailboxes one availability request may fan out
// to (0 restores the built-in default). It is safe to call concurrently with request
// handling, so an operator's edit applies without a restart.
func SetMaxFreeBusyTargets(n int64) {
	if n < 0 {
		n = 0
	}
	freeBusyTargetLimit.Store(n)
}

// maxFreeBusyTargets returns the cap in force.
func maxFreeBusyTargets() int {
	if n := freeBusyTargetLimit.Load(); n > 0 {
		return int(n)
	}
	return defaultFreeBusyTargets
}
