package ews

import (
	"encoding/xml"
	"errors"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// itemRef is one entry of an <m:ItemIds> list ([MS-OXWSCORE] 2.2.4.25): an
// <t:ItemId>, an <t:OccurrenceItemId> naming a series occurrence by its master and
// index, or a <t:RecurringMasterItemId> naming the series of an occurrence.
type itemRef struct {
	ID       string // ItemId Id, or RecurringMasterItemId OccurrenceId
	MasterID string // OccurrenceItemId RecurringMasterId
	Index    int    // OccurrenceItemId InstanceIndex
	Master   bool   // a RecurringMasterItemId
}

// itemRefs is an <m:ItemIds> list read in document order, so each response
// message answers the entry at its position whatever element names it.
type itemRefs struct {
	Items []itemRef
}

// UnmarshalXML reads every child element of the list as one item reference.
func (r *itemRefs) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			ref, err := readItemRef(d, t)
			if err != nil {
				return err
			}
			r.Items = append(r.Items, ref)
		case xml.EndElement:
			return nil
		}
	}
}

// readItemRef reads one item reference element.
func readItemRef(d *xml.Decoder, start xml.StartElement) (itemRef, error) {
	var raw struct {
		ID           string `xml:"Id,attr"`
		MasterID     string `xml:"RecurringMasterId,attr"`
		Index        int    `xml:"InstanceIndex,attr"`
		OccurrenceID string `xml:"OccurrenceId,attr"`
	}
	if err := d.DecodeElement(&raw, &start); err != nil {
		return itemRef{}, err
	}
	switch start.Name.Local {
	case "OccurrenceItemId":
		return itemRef{MasterID: raw.MasterID, Index: raw.Index}, nil
	case "RecurringMasterItemId":
		return itemRef{ID: raw.OccurrenceID, Master: true}, nil
	}
	return itemRef{ID: raw.ID}, nil
}

// resolveItemRef turns an item reference into the item id token it names.
func resolveItemRef(cache *storeCache, sess *session, ref itemRef) (token, code string) {
	switch {
	case ref.Master:
		id, err := oxews.DecodeAnyItemID(ref.ID)
		if err != nil {
			return "", "ErrorInvalidRequest"
		}
		if id.Instance == 0 {
			return "", "ErrorCalendarCannotUseIdForOccurrenceId"
		}
		id.Instance = 0
		return oxews.EncodeItemID(id), ""
	case ref.MasterID != "":
		return occurrenceToken(cache, sess, ref)
	}
	return ref.ID, ""
}

// openReadable opens the mailbox an item id names, gated on the caller's read
// access when it is another's, as GetItem gates it.
func openReadable(cache *storeCache, sess *session, id oxews.ItemID) (*objectstore.Store, string) {
	st, _, isOwn, code := cache.open(sess, id.Mailbox)
	if code == codePublicAbsent {
		code = "ErrorItemNotFound"
	}
	if code != "" {
		return nil, code
	}
	if !isOwn {
		if code := checkItemAccess(st, id, sess.user, mapi.FrightsReadAny); code != "" {
			return nil, code
		}
	}
	return st, ""
}

// occurrenceToken resolves an OccurrenceItemId to the occurrence id of the
// series' index-th occurrence.
func occurrenceToken(cache *storeCache, sess *session, ref itemRef) (token, code string) {
	id, err := oxews.DecodeItemID(ref.MasterID)
	if err != nil {
		return "", "ErrorInvalidRequest"
	}
	st, code := openReadable(cache, sess, id)
	if code != "" {
		return "", code
	}
	msg, err := st.OpenMessage(id.MessageID)
	if err != nil {
		return "", "ErrorItemNotFound"
	}
	reader, err := newCalendarReader(st)
	if err != nil {
		return "", "ErrorInternalServerError"
	}
	if !reader.isSeries(msg.Props) {
		return "", "ErrorCalendarOccurrenceIndexIsOutOfRecurrenceRange"
	}
	at, err := reader.nthOccurrence(msg, ref.Index)
	if errors.Is(err, errNoOccurrence) {
		return "", "ErrorCalendarOccurrenceIndexIsOutOfRecurrenceRange"
	}
	if err != nil {
		return "", "ErrorInternalServerError"
	}
	id.Instance = at.Unix()
	return oxews.EncodeItemID(id), ""
}
