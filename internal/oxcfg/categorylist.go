// Package oxcfg encodes and decodes the FAI configuration data Outlook keeps in a
// mailbox ([MS-OXOCFG]). Only the master category list is modelled so far: it is
// the one configuration item webmail also edits, so both have to read and write
// the same bytes or a user's categories differ depending on which client they open.
package oxcfg

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// CategoryListClass is the message class of the FAI message holding the master
// category list. It lives in the mailbox's Calendar folder.
const CategoryListClass = "IPM.Configuration.CategoryList"

// Category is one entry of the master category list. Name is the join key: it is
// the string a message carries in PidNameKeywords, and the list only supplies the
// colour and Outlook's own bookkeeping.
//
// Every field after Color is bookkeeping hermEX does not use. They are carried
// through an edit unchanged, because dropping them costs the user their keyboard
// shortcuts and Outlook's usage ordering on the first save from webmail.
type Category struct {
	Name  string
	Color int // palette index 0..24; see PaletteHex

	GUID             string
	KeyboardShortcut string
	UsageCount       string
	LastTimeUsed     string
	LastSessionUsed  string
	Other            []xml.Attr // any attribute this version does not model
}

// List is a decoded master category list plus the document attributes that ride
// alongside it, kept so a re-encode does not drop them.
type List struct {
	Categories []Category
	Default    string
	LastSaved  string
}

// PaletteHex maps Outlook's fixed category palette index to a hex colour. The
// stored value is the INDEX, not the colour, so a colour outside this palette
// cannot be represented and snaps to the nearest entry (see NearestPalette).
var PaletteHex = []string{
	"#e7a1a2", "#f9ba89", "#f7dd8f", "#fbf5a1", "#c0e2b1",
	"#adccc0", "#c6d2b0", "#b6cbe4", "#c4b7d5", "#cbb0a1",
	"#b8bcc2", "#b0b0b0", "#d3d3d3", "#a9a9a9", "#000000",
	"#a53a3b", "#c26c33", "#c5a03a", "#c9c04b", "#5b8f4f",
	"#4b8f7f", "#7d8f4b", "#3f6fa5", "#6f5b9c", "#8f5b3f",
}

// xmlCategories is the wire shape of [MS-OXOCFG] 2.2.5.
type xmlCategories struct {
	XMLName    xml.Name      `xml:"categories"`
	Default    string        `xml:"default,attr,omitempty"`
	LastSaved  string        `xml:"lastSavedSession,attr,omitempty"`
	Categories []xmlCategory `xml:"category"`
}

type xmlCategory struct {
	Attrs []xml.Attr `xml:",any,attr"`
}

// modelled names are pulled out of the attribute list into Category fields; every
// other attribute rides in Other and is written back verbatim.
var modelled = map[string]bool{
	"name": true, "color": true, "guid": true,
	"keyboardShortcut": true, "usageCount": true,
	"lastTimeUsed": true, "lastSessionUsed": true,
}

// Decode parses the XML of a category-list configuration message. An empty or
// unparseable stream yields an empty list rather than an error at the call site's
// discretion: the caller decides whether an absent list means "seed one".
func Decode(raw []byte) (List, error) {
	var doc xmlCategories
	if len(strings.TrimSpace(string(raw))) == 0 {
		return List{}, nil
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return List{}, fmt.Errorf("oxcfg: category list: %w", err)
	}
	out := List{Default: doc.Default, LastSaved: doc.LastSaved}
	for _, c := range doc.Categories {
		cat := Category{Color: -1}
		for _, a := range c.Attrs {
			switch a.Name.Local {
			case "name":
				cat.Name = a.Value
			case "color":
				cat.Color = atoiDefault(a.Value, -1)
			case "guid":
				cat.GUID = a.Value
			case "keyboardShortcut":
				cat.KeyboardShortcut = a.Value
			case "usageCount":
				cat.UsageCount = a.Value
			case "lastTimeUsed":
				cat.LastTimeUsed = a.Value
			case "lastSessionUsed":
				cat.LastSessionUsed = a.Value
			default:
				cat.Other = append(cat.Other, a)
			}
		}
		if cat.Name == "" {
			continue // a category with no name is not addressable by a keyword
		}
		out.Categories = append(out.Categories, cat)
	}
	return out, nil
}

// Encode renders the list back to the XML the configuration message carries.
func Encode(l List) ([]byte, error) {
	doc := xmlCategories{Default: l.Default, LastSaved: l.LastSaved}
	for _, c := range l.Categories {
		attrs := []xml.Attr{{Name: xml.Name{Local: "name"}, Value: c.Name}}
		if c.Color >= 0 {
			attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "color"}, Value: itoa(c.Color)})
		}
		for name, v := range map[string]string{
			"keyboardShortcut": c.KeyboardShortcut,
			"usageCount":       c.UsageCount,
			"lastTimeUsed":     c.LastTimeUsed,
			"lastSessionUsed":  c.LastSessionUsed,
			"guid":             c.GUID,
		} {
			if v != "" {
				attrs = append(attrs, xml.Attr{Name: xml.Name{Local: name}, Value: v})
			}
		}
		for _, a := range c.Other {
			if !modelled[a.Name.Local] {
				attrs = append(attrs, xml.Attr{Name: xml.Name{Local: a.Name.Local}, Value: a.Value})
			}
		}
		sortAttrs(attrs)
		doc.Categories = append(doc.Categories, xmlCategory{Attrs: attrs})
	}
	body, err := xml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("oxcfg: category list: %w", err)
	}
	return append([]byte(xml.Header), body...), nil
}
