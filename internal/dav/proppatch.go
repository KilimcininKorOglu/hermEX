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
	isCard := strings.HasPrefix(r.URL.Path, "/dav/addressbooks/")
	if (!isCal && !isCard) || (isCal && kind != kindCalendar) || (isCard && kind != kindAddressbook) {
		http.Error(w, "PROPPATCH is supported only on collections", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, s.vcardLimit()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var pu propUpdate
	if err := xml.Unmarshal(body, &pu); err != nil {
		http.Error(w, "invalid propertyupdate: "+err.Error(), http.StatusBadRequest)
		return
	}

	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	var fid int64
	var ok bool
	if isCal {
		fid, ok, err = calCollectionFID(st, coll)
	} else {
		fid, ok, err = cardCollectionFID(st, coll)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such collection", http.StatusNotFound)
		return
	}

	type instruction struct {
		remove bool
		key    string
		node   rawProp
	}
	var instrs []instruction
	var protected []string
	for _, op := range pu.Ops {
		remove := op.XMLName.Local == "remove"
		for _, n := range op.Prop.Nodes {
			key := propKey(n.XMLName)
			instrs = append(instrs, instruction{remove: remove, key: key, node: n})
			if protectedProps[key] {
				protected = append(protected, key)
			}
		}
	}

	// Atomic failure: a protected property poisons the whole request.
	if len(protected) > 0 {
		var bad, dependent []string
		for _, in := range instrs {
			if protectedProps[in.key] {
				bad = append(bad, propNameElement(in.node.XMLName))
			} else {
				dependent = append(dependent, propNameElement(in.node.XMLName))
			}
		}
		resp := msResponse{Href: r.URL.Path}
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
		writeMultistatus(w, &multistatus{Responses: []msResponse{resp}})
		return
	}

	// Apply in document order.
	var okNames []string
	for _, in := range instrs {
		if in.remove {
			if err := st.RemoveDeadProp(fid, in.key); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if err := st.SetDeadProp(fid, in.key, propValueElement(in.node)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		okNames = append(okNames, propNameElement(in.node.XMLName))
	}
	resp := msResponse{
		Href: r.URL.Path,
		Propstat: []msPropstat{{
			Prop:   msProp{Extra: []byte(strings.Join(okNames, ""))},
			Status: statusOK,
		}},
	}
	writeMultistatus(w, &multistatus{Responses: []msResponse{resp}})
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
