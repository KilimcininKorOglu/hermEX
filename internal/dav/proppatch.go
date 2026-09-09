package dav

import (
	"encoding/xml"
	"io"
	"net/http"
	"strings"

	"hermex/internal/objectstore"
)

// propUpdate is a PROPPATCH request body (RFC 4918 §14.19): an ordered list of set
// and remove instructions, each carrying arbitrary property elements.
type propUpdate struct {
	XMLName xml.Name       `xml:"DAV: propertyupdate"`
	Ops     []propUpdateOp `xml:",any"`
}

// propUpdateOp is one <set> or <remove> instruction; XMLName.Local distinguishes them.
type propUpdateOp struct {
	XMLName xml.Name
	Prop    propNodes `xml:"DAV: prop"`
}

// propNodes captures the arbitrary property elements inside a <prop>.
type propNodes struct {
	Nodes []rawProp `xml:",any"`
}

// rawProp captures one property element: its qualified name and verbatim content.
type rawProp struct {
	XMLName  xml.Name
	InnerXML string `xml:",innerxml"`
}

// protectedProps are live/computed properties a client may not set or remove; an
// attempt fails the whole PROPPATCH (RFC 4918 §9.2, cannot-modify-protected-property).
var protectedProps = map[string]bool{
	nsDAV + " resourcetype":                        true,
	nsDAV + " getetag":                             true,
	nsDAV + " getcontentlength":                    true,
	nsDAV + " getcontenttype":                      true,
	nsDAV + " getlastmodified":                     true,
	nsDAV + " lockdiscovery":                       true,
	nsDAV + " supportedlock":                       true,
	nsDAV + " supported-report-set":                true,
	nsDAV + " sync-token":                          true,
	nsDAV + " current-user-principal":              true,
	nsDAV + " principal-URL":                       true,
	nsDAV + " current-user-privilege-set":          true,
	nsDAV + " owner":                               true,
	nsDAV + " quota-used-bytes":                    true,
	nsDAV + " quota-available-bytes":               true,
	nsCS + " getctag":                              true,
	nsCalDAV + " supported-calendar-component-set": true,
}

// displayNameKey is the one computed property a client may also set; storing it as a
// dead property lets PROPFIND replay the client's label in place of the default.
const displayNameKey = nsDAV + " displayname"

// handleProppatch sets and removes WebDAV dead properties on a calendar or address
// book collection (RFC 4918 §9.2). Instructions are applied atomically: if any names
// a protected property, nothing changes and the response reports 403 for those and
// 424 for the rest.
func (s *Server) handleProppatch(w http.ResponseWriter, r *http.Request, mailbox string) {
	kind, _, coll, _ := classify(r.URL.Path)
	isCal := strings.HasPrefix(r.URL.Path, "/dav/calendars/")
	if !proppatchableCollection(kind, isCal, strings.HasPrefix(r.URL.Path, "/dav/addressbooks/")) {
		http.Error(w, "PROPPATCH is supported only on collections", http.StatusForbidden)
		return
	}

	pu, ok := s.parsePropUpdate(w, r)
	if !ok {
		return
	}

	st, err := objectstore.Open(mailbox)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	defer st.Close()

	fid, found, err := collectionByKind(st, isCal, coll)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such collection", http.StatusNotFound)
		return
	}

	instrs := proppatchInstructions(pu)
	// Atomic failure: a protected property poisons the whole request.
	if hasProtected(instrs) {
		writeMultistatus(w, protectedRefusal(r.URL.Path, instrs))
		return
	}

	okNames, err := applyProppatch(st, fid, instrs)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	writeMultistatus(w, &multistatus{Responses: []msResponse{{
		Href: r.URL.Path,
		Propstat: []msPropstat{{
			Prop:   msProp{Extra: []byte(strings.Join(okNames, ""))},
			Status: statusOK,
		}},
	}}})
}

// proppatchableCollection reports whether the path names a collection PROPPATCH may
// write to. The dead properties live on the collection folder, so an object path or
// a home set is refused.
func proppatchableCollection(kind resourceKind, isCal, isCard bool) bool {
	switch {
	case isCal:
		return kind == kindCalendar
	case isCard:
		return kind == kindAddressbook
	default:
		return false
	}
}

// parsePropUpdate reads and unmarshals the PROPPATCH body. On failure the response
// is already written.
func (s *Server) parsePropUpdate(w http.ResponseWriter, r *http.Request) (propUpdate, bool) {
	var pu propUpdate
	body, err := io.ReadAll(io.LimitReader(r.Body, s.vcardLimit()))
	if err != nil {
		s.davError(w, err, http.StatusBadRequest)
		return pu, false
	}
	if err := xml.Unmarshal(body, &pu); err != nil {
		s.davError(w, err, http.StatusBadRequest)
		return pu, false
	}
	return pu, true
}

// proppatchInstruction is one set or remove the request asked for.
type proppatchInstruction struct {
	remove bool
	key    string
	node   rawProp
}

// proppatchInstructions flattens the request's set/remove operations into one
// list, in document order, which is the order they are applied in.
func proppatchInstructions(pu propUpdate) []proppatchInstruction {
	var instrs []proppatchInstruction
	for _, op := range pu.Ops {
		remove := op.XMLName.Local == "remove"
		for _, n := range op.Prop.Nodes {
			instrs = append(instrs, proppatchInstruction{remove: remove, key: propKey(n.XMLName), node: n})
		}
	}
	return instrs
}

// hasProtected reports whether any instruction names a property the server owns.
func hasProtected(instrs []proppatchInstruction) bool {
	for _, in := range instrs {
		if protectedProps[in.key] {
			return true
		}
	}
	return false
}

// protectedRefusal builds the multistatus refusing the whole request: the
// protected properties are forbidden and every other one fails as a dependency.
func protectedRefusal(href string, instrs []proppatchInstruction) *multistatus {
	var bad, dependent []string
	for _, in := range instrs {
		if protectedProps[in.key] {
			bad = append(bad, propNameElement(in.node.XMLName))
		} else {
			dependent = append(dependent, propNameElement(in.node.XMLName))
		}
	}
	resp := msResponse{Href: href}
	resp.Propstat = append(resp.Propstat, msPropstat{
		Prop:   msProp{Extra: []byte(strings.Join(bad, ""))},
		Status: statusForbidden,
	})
	if len(dependent) > 0 {
		resp.Propstat = append(resp.Propstat, msPropstat{
			Prop:   msProp{Extra: []byte(strings.Join(dependent, ""))},
			Status: statusFailedDependency,
		})
	}
	return &multistatus{Responses: []msResponse{resp}}
}

// applyProppatch writes every instruction to the collection's dead properties and
// returns the property-name elements the response reports as applied.
func applyProppatch(st *objectstore.Store, fid int64, instrs []proppatchInstruction) ([]string, error) {
	var okNames []string
	for _, in := range instrs {
		if in.remove {
			if err := st.RemoveDeadProp(fid, in.key); err != nil {
				return nil, err
			}
		} else if err := st.SetDeadProp(fid, in.key, propValueElement(in.node)); err != nil {
			return nil, err
		}
		okNames = append(okNames, propNameElement(in.node.XMLName))
	}
	return okNames, nil
}

// applyDeadProps attaches a collection's stored dead properties to a PROPFIND prop
// set: each is appended verbatim, and a stored DAV:displayname replaces the computed
// one so the client's chosen label is not duplicated.
func applyDeadProps(prop *msProp, dead []objectstore.DeadProp) {
	if len(dead) == 0 {
		return
	}
	var b strings.Builder
	for _, d := range dead {
		if d.Name == displayNameKey {
			prop.DisplayName = ""
		}
		b.WriteString(d.Raw)
	}
	prop.Extra = []byte(b.String())
}

// propKey is the "{namespace} local" identity of a property element.
func propKey(n xml.Name) string { return n.Space + " " + n.Local }

// propNameElement renders an empty property element (for a propstat <prop>), with
// the namespace as a default declaration so it stands alone.
func propNameElement(n xml.Name) string {
	if n.Space == "" {
		return "<" + n.Local + "/>"
	}
	return "<" + n.Local + ` xmlns="` + escapeAttr(n.Space) + `"/>`
}

// propValueElement renders a property element with its stored value, replayed
// verbatim by PROPFIND.
func propValueElement(p rawProp) string {
	if p.XMLName.Space == "" {
		return "<" + p.XMLName.Local + ">" + p.InnerXML + "</" + p.XMLName.Local + ">"
	}
	return "<" + p.XMLName.Local + ` xmlns="` + escapeAttr(p.XMLName.Space) + `">` +
		p.InnerXML + "</" + p.XMLName.Local + ">"
}

// escapeAttr escapes the XML attribute-significant characters in a namespace URI.
func escapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", `"`, "&quot;")
	return r.Replace(s)
}
