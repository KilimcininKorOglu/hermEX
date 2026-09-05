package oxcmail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"hermex/internal/mapi"
)

// maxInlineDataURIs bounds how many data: images one body may carry. A composed
// message reaches this only through pasted pictures, and each one is held in
// memory twice while it is decoded, so an unbounded count is a memory cost the
// sender never chose.
const maxInlineDataURIs = 64

// InlineDataURIs rewrites every `data:` image in an HTML body into an inline
// attachment referenced by `cid:`, and returns the rewritten body with those
// attachments.
//
// A body carrying its pictures as data: URIs is not deliverable mail: Outlook
// and Gmail refuse to render a data: image in a received message, so the sender
// sees their picture and the recipient sees a gap. The exporter already renders
// an attachment carrying PR_ATTACH_CONTENT_ID with the ATT_MHTML_REF flag into a
// multipart/related alongside the body, which is the shape those clients do
// render, so this converts into that shape rather than inventing another.
//
// idFor mints one Content-ID per image; the caller supplies it so the ids are
// unique within the message and reproducible in a test.
func InlineDataURIs(htmlBody string, idFor func(n int) string) (string, []Attachment, error) {
	if !strings.Contains(htmlBody, "data:image/") {
		return htmlBody, nil, nil
	}
	nodes, err := html.ParseFragment(strings.NewReader(htmlBody), &html.Node{
		Type: html.ElementNode, Data: "body", DataAtom: atom.Body,
	})
	if err != nil {
		// An unparseable body is left exactly as it came, because rewriting half
		// of it would corrupt the message the sender wrote.
		return htmlBody, nil, nil
	}

	var atts []Attachment
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "img" {
			rewriteImg(n, idFor, &atts)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	if len(atts) == 0 {
		return htmlBody, nil, nil
	}

	var out bytes.Buffer
	for _, n := range nodes {
		if err := html.Render(&out, n); err != nil {
			return "", nil, fmt.Errorf("oxcmail: inline data uri: %w", err)
		}
	}
	return out.String(), atts, nil
}

// rewriteImg replaces one img's data: src with a cid: reference, appending the
// attachment it now points at. An image this cannot decode keeps its src, so a
// malformed one degrades to what it already was rather than losing the element.
func rewriteImg(n *html.Node, idFor func(n int) string, atts *[]Attachment) {
	for i, a := range n.Attr {
		if !strings.EqualFold(a.Key, "src") {
			continue
		}
		if len(*atts) >= maxInlineDataURIs {
			return
		}
		mimeType, data, ok := decodeDataURI(a.Val)
		if !ok {
			return
		}
		cid := idFor(len(*atts))
		n.Attr[i].Val = "cid:" + cid
		*atts = append(*atts, inlineAttachment(cid, mimeType, data, len(*atts)+1))
		return
	}
}

// inlineAttachment builds the property bag the exporter renders as an inline,
// Content-ID-carrying part of a multipart/related.
func inlineAttachment(cid, mimeType string, data []byte, n int) Attachment {
	var p mapi.PropertyValues
	p.Set(mapi.PrAttachMethod, int32(mapi.AttachByValue))
	p.Set(mapi.PrAttachDataBin, data)
	p.Set(mapi.PrAttachMimeTag, mimeType)
	p.Set(mapi.PrAttachContentID, cid)
	p.Set(mapi.PrAttachFlags, int32(mapi.AttMhtmlRef))
	// A name is not required for an inline picture, but a recipient saving one
	// out of the message gets a usable file rather than an unnamed blob.
	p.Set(mapi.PrAttachLongFilename, fmt.Sprintf("image%03d%s", n, extensionFor(mimeType)))
	return Attachment{Props: p}
}

// decodeDataURI reads "data:<mime>;base64,<data>". Only base64 image payloads are
// accepted, because that is the single form a browser produces for a pasted
// picture, and anything else in a src is not one.
func decodeDataURI(v string) (mimeType string, data []byte, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(strings.ToLower(v), prefix) {
		return "", nil, false
	}
	meta, payload, found := strings.Cut(v[len(prefix):], ",")
	if !found {
		return "", nil, false
	}
	meta = strings.ToLower(strings.TrimSpace(meta))
	if !strings.HasSuffix(meta, ";base64") {
		return "", nil, false
	}
	mimeType = strings.TrimSuffix(meta, ";base64")
	if !strings.HasPrefix(mimeType, "image/") {
		return "", nil, false
	}
	// A browser wraps a long data: URI across lines in the markup, so the payload
	// arrives with whitespace that strict base64 refuses.
	payload = strings.Join(strings.Fields(payload), "")
	b, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(b) == 0 {
		return "", nil, false
	}
	return mimeType, b, true
}

// extensionFor maps an image type to the file extension a saved copy should
// carry. An unknown type gets .img rather than a guess.
func extensionFor(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	}
	return ".img"
}
