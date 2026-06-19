package activesync

import (
	"strconv"
	"strings"

	"hermex/internal/mime"
	"hermex/internal/wbxml"
)

// attachMethodNormal is the AirSyncBase attachment Method for a normal by-value
// attachment (MS-ASAIRS 2.2.2.20).
const attachMethodNormal = "1"

// attachInfo is one attachment surfaced from a message's MIME: its display name,
// content type, encoded size, and position among the message's attachments (the
// index a FileReference resolves back to).
type attachInfo struct {
	displayName string
	contentType string
	size        int
	index       int
}

// messageAttachments walks a message's MIME tree and returns its attachments in
// depth-first order. v1 surfaces parts marked Content-Disposition: attachment;
// inline parts (the body text, inline images) are not listed.
func messageAttachments(raw []byte) []attachInfo {
	var out []attachInfo
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil {
			return
		}
		if len(p.Children) == 0 {
			if isAttachment(p) {
				out = append(out, attachInfo{
					displayName: attachName(p, len(out)),
					contentType: p.Type + "/" + p.Subtype,
					size:        p.Size,
					index:       len(out),
				})
			}
			return
		}
		for _, c := range p.Children {
			walk(c)
		}
	}
	walk(mime.ParseStructure(raw))
	return out
}

// isAttachment reports whether a leaf MIME part is an attachment to surface.
func isAttachment(p *mime.Part) bool {
	return p.Disposition == "attachment"
}

// attachName returns an attachment's display name, synthesizing one when the part
// carries no file name.
func attachName(p *mime.Part, index int) string {
	if name := p.Filename(); name != "" {
		return name
	}
	return "attachment" + strconv.Itoa(index+1)
}

// fileRef encodes the opaque FileReference a client echoes back to fetch one
// attachment: the collection id, the message server id, and the attachment index.
func fileRef(collID, serverID string, index int) string {
	return collID + ":" + serverID + ":" + strconv.Itoa(index)
}

// parseFileRef decodes a FileReference back into the collection id, message
// server id, and attachment index it names.
func parseFileRef(ref string) (collID, serverID string, index int, ok bool) {
	parts := strings.SplitN(ref, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", "", 0, false
	}
	idx, err := strconv.Atoi(parts[2])
	if err != nil || idx < 0 {
		return "", "", 0, false
	}
	return parts[0], parts[1], idx, true
}

// attachmentContent returns the decoded bytes and content type of the index-th
// attachment in a message's MIME, or ok=false when no such attachment exists.
func attachmentContent(raw []byte, index int) (data []byte, contentType string, ok bool) {
	n := 0
	var found *mime.Part
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if found != nil || p == nil {
			return
		}
		if len(p.Children) == 0 {
			if isAttachment(p) {
				if n == index {
					found = p
				}
				n++
			}
			return
		}
		for _, c := range p.Children {
			walk(c)
		}
	}
	walk(mime.ParseStructure(raw))
	if found == nil {
		return nil, "", false
	}
	dec, err := found.DecodedContent()
	if err != nil {
		return nil, "", false
	}
	return dec, found.Type + "/" + found.Subtype, true
}

// attachmentsNode builds the AirSyncBase Attachments element listing a message's
// attachments, or nil when it has none.
func attachmentsNode(collID, serverID string, atts []attachInfo) *wbxml.Node {
	if len(atts) == 0 {
		return nil
	}
	items := make([]*wbxml.Node, 0, len(atts))
	for _, a := range atts {
		items = append(items, wbxml.Elem(wbxml.ABAttachment,
			wbxml.Str(wbxml.ABAttDisplayName, a.displayName),
			wbxml.Str(wbxml.ABFileReference, fileRef(collID, serverID, a.index)),
			wbxml.Str(wbxml.ABMethod, attachMethodNormal),
			wbxml.Str(wbxml.ABEstimatedDataSize, strconv.Itoa(a.size)),
		))
	}
	return wbxml.Elem(wbxml.ABAttachments, items...)
}
