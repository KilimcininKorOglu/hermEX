package activesync

import (
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// MS-ASNOTE: a stored IPM.StickyNote maps to and from the AirSync Notes class. A note
// is the title (PR_SUBJECT), the body (PR_BODY, carried under AirSyncBase), the shared
// category keywords, and the last-modified time. These are the same properties the
// web backend reads and writes, so a note is one object across every protocol.

const noteClass = "IPM.StickyNote"

// noteAppData builds the AirSync ApplicationData for a stored note.
func noteAppData(st *objectstore.Store, objectID int64) (*wbxml.Node, error) {
	msg, err := st.OpenMessage(objectID)
	if err != nil {
		return nil, err
	}
	if contactStr(msg.Props, mapi.PrMessageClass) != noteClass {
		return nil, nil // not a note; nothing to stream
	}
	data := wbxml.Elem(wbxml.ASData)
	if subj := contactStr(msg.Props, mapi.PrSubject); subj != "" {
		data.Children = append(data.Children, wbxml.Str(wbxml.NTSubject, subj))
	}
	data.Children = append(data.Children, wbxml.Str(wbxml.NTMessageClass, noteClass))
	if t, ok := ntTimeProp(msg.Props, mapi.PrLastModificationTime); ok {
		data.Children = append(data.Children, wbxml.Str(wbxml.NTLastModified, t.UTC().Format(easContactDate)))
	}
	if cats := keywordsOf(st, msg.Props); len(cats) > 0 {
		nodes := make([]*wbxml.Node, 0, len(cats))
		for _, c := range cats {
			nodes = append(nodes, wbxml.Str(wbxml.NTCategory, c))
		}
		data.Children = append(data.Children, wbxml.Elem(wbxml.NTCategories, nodes...))
	}
	if body := contactStr(msg.Props, mapi.PrBody); body != "" {
		data.Children = append(data.Children, wbxml.Elem(wbxml.ABBody,
			wbxml.Str(wbxml.ABType, "1"),
			wbxml.Str(wbxml.ABEstimatedDataSize, strconv.Itoa(len(body))),
			wbxml.Opaque(wbxml.ABData, []byte(body))))
	}
	return data, nil
}

// parseNoteItem decodes a device's note ApplicationData into MAPI properties.
func parseNoteItem(st *objectstore.Store, data *wbxml.Node) (mapi.PropertyValues, error) {
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, noteClass)
	if subj := data.ChildText(wbxml.NTSubject); subj != "" {
		props.Set(mapi.PrSubject, subj)
	}
	if body := airSyncBody(data); body != "" {
		props.Set(mapi.PrBody, body)
	}
	if cats := noteCategories(data); len(cats) > 0 {
		ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameKeywords})
		if err != nil {
			return nil, err
		}
		if ids[0] != 0 {
			props.Set(mapi.MakeTag(ids[0], mapi.PtMvUnicode), cats)
		}
	}
	return props, nil
}

// airSyncBody extracts an item's AirSyncBase body text, sent either opaque or as
// plain text. Notes and tasks carry their body the same way.
func airSyncBody(data *wbxml.Node) string {
	body := data.Child(wbxml.ABBody)
	if body == nil {
		return ""
	}
	d := body.Child(wbxml.ABData)
	if d == nil {
		return ""
	}
	if len(d.Opaque) > 0 {
		return string(d.Opaque)
	}
	return d.Text
}

// noteCategories reads the device's Categories list.
func noteCategories(data *wbxml.Node) []string {
	return listChildText(data, wbxml.NTCategories, wbxml.NTCategory)
}

// keywordsOf reads a message's category keywords (the shared multivalue named
// property), or nil when absent.
func keywordsOf(st *objectstore.Store, props mapi.PropertyValues) []string {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameKeywords})
	if err != nil || ids[0] == 0 {
		return nil
	}
	if v, ok := props.Get(mapi.MakeTag(ids[0], mapi.PtMvUnicode)); ok {
		if cats, ok := v.([]string); ok {
			return cats
		}
	}
	return nil
}
