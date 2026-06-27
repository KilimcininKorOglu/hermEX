package oxews

import (
	"encoding/xml"
	"time"
)

// NoteClass is the store message class for a sticky note.
const NoteClass = "IPM.StickyNote"

// Item is the EWS base <t:Item> element. EWS defines no dedicated sticky-note type, so
// a note is returned as a base Item carrying ItemClass="IPM.StickyNote", with its
// children in the Types.xsd sequence order.
type Item struct {
	XMLName          xml.Name    `xml:"http://schemas.microsoft.com/exchange/services/2006/types Item"`
	ItemID           ItemIDElem  `xml:"ItemId"`
	ItemClass        string      `xml:"ItemClass,omitempty"`
	Subject          string      `xml:"Subject,omitempty"`
	Body             *Body       `xml:"Body,omitempty"`
	Categories       *Categories `xml:"Categories,omitempty"`
	LastModifiedTime string      `xml:"LastModifiedTime,omitempty"`
}

// BuildNote renders a sticky note as a base <t:Item> from its extracted fields plus
// the item metadata.
func BuildNote(meta ItemMeta, subject, body string, categories []string, lastMod time.Time) Item {
	it := Item{
		ItemID:    ItemIDElem{ID: meta.ItemID, ChangeKey: meta.ChangeKey},
		ItemClass: NoteClass,
		Subject:   subject,
	}
	if body != "" {
		it.Body = &Body{BodyType: "Text", Content: body}
	}
	if len(categories) > 0 {
		it.Categories = &Categories{String: categories}
	}
	if !lastMod.IsZero() {
		it.LastModifiedTime = lastMod.UTC().Format(ewsTime)
	}
	return it
}
