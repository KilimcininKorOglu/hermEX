package activesync

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// resolveRecipientLimit caps the GAL matches returned for one recipient query.
const resolveRecipientLimit = 100

// ResolveRecipients Status values (MS-ASCMD 2.2.3.166.2): the overall command
// status, the per-query resolution status, and the per-picture status.
const (
	rrStatusOK            = 1 // the command succeeded
	rrStatusProtocolError = 5 // an invalid parameter, or a range over its limit
	rrStatusServerError   = 6 // the server could not resolve the recipients
	rrResolved            = 1 // exactly one recipient matched the query
	rrAmbiguous           = 2 // several matched; the list is every suggestion
	rrAmbiguousPartial    = 3 // several matched; the list is cut, RecipientCount is the total
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
	invalid     bool // a cap that is not an unsigned integer
	// maxAmbiguous is the suggestions returned per query: Options>
	// MaxAmbiguousRecipients when the client sent it, else resolveRecipientLimit,
	// never above it; -1 marks a value outside 0-9999 ([MS-ASCMD] 2.2.3.100).
	maxAmbiguous int
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
	tos, opt, win := parseResolveRequest(root)
	if len(tos) == 0 || win.invalid || opt.invalid || opt.maxAmbiguous < 0 {
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
	fail := func(stage string, err error) { s.logDirectoryFail("resolve."+stage+".fail", sess, err) }
	for _, to := range tos {
		node, err := resolveOneRecipient(gal, to, opt, win, sess, &pictures, fail)
		if err != nil {
			// A directory failure is a server error, never an unknown recipient.
			s.logResolveFail(sess, err)
			writeWBXML(w, wbxml.Elem(wbxml.RRResolveRecipients,
				wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrStatusServerError))))
			return
		}
		children = append(children, node)
	}
	writeWBXML(w, wbxml.Elem(wbxml.RRResolveRecipients, children...))
}

// parseResolveRequest reads a ResolveRecipients request: its To queries and the
// picture and availability options.
func parseResolveRequest(root *wbxml.Node) (tos []string, opt pictureOpts, win availabilityWindow) {
	for _, c := range root.Children {
		switch c.Tag {
		case wbxml.RRTo:
			tos = append(tos, c.Text)
		case wbxml.RROptions:
			opt = parsePictureOpts(c)
			win = parseAvailability(c)
		}
	}
	opt.maxAmbiguous = parseMaxAmbiguous(root.Child(wbxml.RROptions))
	return tos, opt, win
}

// parseMaxAmbiguous reads Options>MaxAmbiguousRecipients, an integer in 0-9999
// ([MS-ASCMD] 2.2.3.100), as the suggestions to return per query, capped at
// resolveRecipientLimit. Absent, it is the server cap; outside the range or not
// a number, it is -1, which the command answers as a protocol error.
func parseMaxAmbiguous(options *wbxml.Node) int {
	if options == nil || options.Child(wbxml.RRMaxAmbiguous) == nil {
		return resolveRecipientLimit
	}
	n, err := strconv.ParseUint(options.ChildText(wbxml.RRMaxAmbiguous), 10, 16)
	if err != nil || n > 9999 {
		return -1
	}
	return min(int(n), resolveRecipientLimit)
}

// parsePictureOpts reads the Options>Picture request: its presence means
// portraits are wanted, with optional MaxSize/MaxPictures caps. Both caps are
// unsigned integers in [MS-ASCMD]; one that is not marks the options invalid,
// because reading it as zero would lift the cap the client meant to set.
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
				o.maxSize = o.pictureCap(p.Text)
			case wbxml.RRMaxPictures:
				o.maxPictures = o.pictureCap(p.Text)
			}
		}
	}
	return o
}

// pictureCap parses one cap as an unsigned 31-bit integer, the range an int holds
// on every platform, and marks the options invalid when it is not one.
func (o *pictureOpts) pictureCap(text string) int {
	n, err := strconv.ParseUint(text, 10, 31)
	if err != nil {
		o.invalid = true
		return 0
	}
	return int(n)
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
//
// The mailbox reads behind a contact match and a picture fail open, because the
// address-book half has already answered; fail records each such failure, so a
// store that cannot be read does not look like an empty one.
func resolveOneRecipient(gal directory.GAL, to string, opt pictureOpts, win availabilityWindow, sess *session, pictures *int, fail storeFail) (*wbxml.Node, error) {
	resp := []*wbxml.Node{wbxml.Str(wbxml.RRTo, to)}

	matches, total, err := resolveEntries(gal, to, sess, fail, opt.maxAmbiguous)
	if err != nil {
		return nil, err
	}
	resp = append(resp,
		wbxml.Str(wbxml.RRStatus, strconv.Itoa(resolutionStatus(len(matches), total))),
		wbxml.Str(wbxml.RRRecipientCount, strconv.Itoa(total)))
	for _, m := range matches {
		resp = append(resp, recipientNode(m, opt, win, sess, pictures, fail))
	}
	return wbxml.Elem(wbxml.RRResponse, resp...), nil
}

// resolutionStatus is a query's Response Status ([MS-ASCMD] 2.2.3.177.12) from
// the recipients returned and the recipients that matched: none matched, one
// resolved, several returned in full, or several returned in part.
func resolutionStatus(returned, total int) int {
	switch {
	case total == 0:
		return rrUnresolved
	case total == 1:
		return rrResolved
	case returned < total:
		return rrAmbiguousPartial
	default:
		return rrAmbiguous
	}
}

// galResolveHide is the hide bits name resolution honors.
const galResolveHide = directory.HideFromGAL | directory.HideFromResolve

// resolveEntries matches a query against the two address sources a client
// expects: the organization's address book, then the caller's own contacts. The
// address book comes first, because a colleague is the likelier match, and an
// address in both is reported once. It returns at most limit matches (always
// the one match a query with a single match has) and how many matched in all.
func resolveEntries(gal directory.GAL, to string, sess *session, fail storeFail, limit int) ([]resolvedRecipient, int, error) {
	if to == "" {
		return nil, 0, nil
	}
	set := recipientSet{seen: map[string]bool{}}
	galFull := false
	if gal != nil {
		entries, err := gal.SearchGAL(sess.user, to, limit+1, galResolveHide)
		if err != nil {
			return nil, 0, err
		}
		galFull = len(entries) > limit
		// Withhold the addresses the operator hid from name resolution.
		for _, e := range directory.ResolvableGAL(entries) {
			set.add(e, false)
		}
	}
	fromGAL := len(set.list)
	for _, c := range ownContacts(sess.mailbox, to, limit+1, fail) {
		set.add(directory.GALEntry{DisplayName: c.DisplayName, Address: c.Address}, true)
	}
	total, err := matchTotal(gal, sess.user, to, len(set.list), fromGAL, galFull)
	if err != nil {
		return nil, 0, err
	}
	return set.list[:min(len(set.list), max(limit, 1))], total, nil
}

// recipientSet collects matches once per address.
type recipientSet struct {
	list []resolvedRecipient
	seen map[string]bool
}

func (s *recipientSet) add(e directory.GALEntry, contact bool) {
	key := strings.ToLower(strings.TrimSpace(e.Address))
	if key == "" || s.seen[key] {
		return
	}
	s.seen[key] = true
	s.list = append(s.list, resolvedRecipient{entry: e, contact: contact})
}

// matchTotal is how many recipients matched in all: the matches collected, plus,
// when the address-book page was cut, the address-book entries past it, counted
// by the directory. A directory that cannot count leaves the total at what was
// collected.
func matchTotal(gal directory.GAL, caller, to string, collected, fromGAL int, galFull bool) (int, error) {
	counter, ok := gal.(directory.GALCounter)
	if !galFull || !ok {
		return collected, nil
	}
	n, err := counter.CountGAL(caller, to, galResolveHide)
	if err != nil {
		return 0, err
	}
	return collected + max(n-fromGAL, 0), nil
}

// storeFail records a mailbox read that failed under the named stage.
type storeFail func(stage string, err error)

// closeOrFail closes a store the lookup opened and reports a failed close
// through fail under the lookup's stage.
func closeOrFail(st *objectstore.Store, stage string, fail storeFail) {
	if err := st.Close(); err != nil {
		fail(stage+".close", err)
	}
}

// ownContacts matches a query against the caller's own Contacts folder. A mailbox
// that will not open yields nothing rather than failing the query, because the
// address-book half has already answered; the failure is recorded. A query
// shorter than three characters matches nothing (SearchContacts holds the address
// book back while the user is still typing).
func ownContacts(mailbox, query string, limit int, fail storeFail) []objectstore.ContactMatch {
	if mailbox == "" || limit <= 0 {
		return nil
	}
	st, err := objectstore.OpenExisting(mailbox)
	if err != nil {
		fail("contacts", err)
		return nil
	}
	defer closeOrFail(st, "contacts", fail)
	matches, err := st.SearchContacts(query, limit)
	if err != nil {
		fail("contacts", err)
		return nil
	}
	return matches
}

// recipientNode builds one Recipient: its type, name and address, plus the
// free/busy and picture the client asked for.
func recipientNode(m resolvedRecipient, opt pictureOpts, win availabilityWindow, sess *session, pictures *int, fail storeFail) *wbxml.Node {
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
		rc = append(rc, pictureNode(m, opt, sess, pictures, fail))
	}
	return wbxml.Elem(wbxml.RRRecipient, rc...)
}

// pictureNode builds a recipient's Picture element, serving the portrait from the
// cross-protocol photo property (or, for one of the caller's own contacts, from
// that contact's card) and honoring the MaxSize/MaxPictures caps.
func pictureNode(m resolvedRecipient, opt pictureOpts, sess *session, pictures *int, fail storeFail) *wbxml.Node {
	if opt.maxPictures > 0 && *pictures >= opt.maxPictures {
		return wbxml.Elem(wbxml.RRPicture, wbxml.Str(wbxml.RRStatus, strconv.Itoa(rrPictureLimit)))
	}
	var photo []byte
	if m.contact {
		photo = contactPhoto(sess.mailbox, m.entry.Address, fail)
	} else {
		photo = recipientPhoto(m.entry.StorePath, fail)
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

// recipientPhoto opens a mailbox and returns its portrait bytes, or nil. A
// directory entry whose mailbox was never provisioned has no portrait, which is
// not a failure; every other read error is recorded.
func recipientPhoto(storePath string, fail storeFail) []byte {
	if storePath == "" {
		return nil
	}
	st, err := objectstore.OpenExisting(storePath)
	if errors.Is(err, objectstore.ErrNotProvisioned) {
		return nil
	}
	if err != nil {
		fail("photo", err)
		return nil
	}
	defer closeOrFail(st, "photo", fail)
	p, err := st.UserPhoto()
	if err != nil {
		fail("photo", err)
	}
	return p
}

// contactPhoto returns the picture the caller saved on their own contact card for
// an address, or nil. The store read is the caller's own mailbox, and a read that
// fails is recorded.
func contactPhoto(mailbox, address string, fail storeFail) []byte {
	if mailbox == "" {
		return nil
	}
	st, err := objectstore.OpenExisting(mailbox)
	if err != nil {
		fail("contact-photo", err)
		return nil
	}
	defer closeOrFail(st, "contact-photo", fail)
	p, err := st.ContactPhotoFor(address)
	if err != nil {
		fail("contact-photo", err)
	}
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
