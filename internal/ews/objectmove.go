package ews

import (
	"errors"

	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// moveCopyObject moves or copies an item the object store alone holds (a calendar
// item, a task, a note) into the destination. A move keeps the item's id, so
// every other protocol still names it the same; a copy stores a new object. A move
// of an item in the Recoverable Items dumpster restores it there, as it does for
// mail.
func moveCopyObject(dest moveCopyDest, id oxews.ItemID, remove bool) itemResponseMessage {
	newID, err := placeObject(dest, id.MessageID, remove)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}
	ref := oxews.ItemIDElem{
		ID:        oxews.EncodeItemID(oxews.ItemID{FolderID: dest.fid, MessageID: newID, Mailbox: dest.mailbox}),
		ChangeKey: changeKey(dest.st, newID),
	}
	return itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", Items: movedObject(dest.st, newID, ref)}
}

// placeObject performs the move or copy and returns the id the item has after it.
func placeObject(dest moveCopyDest, mid int64, remove bool) (int64, error) {
	if !remove {
		msg, err := dest.st.OpenMessage(mid)
		if err != nil {
			return 0, err
		}
		return dest.st.CreateMessage(dest.fid, msg)
	}
	fid, err := dest.st.MessageFolder(mid)
	if err != nil {
		return 0, err
	}
	_, err = dest.st.MoveMessageImport(fid, mid, dest.fid, mid)
	if errors.Is(err, objectstore.ErrObjectDeleted) {
		info, err := dest.st.RecoverMessageTo(mid, dest.fid)
		return info.ID, err
	}
	return mid, err
}

// movedObject wraps the moved or copied item's id in the element of its kind.
func movedObject(st *objectstore.Store, id int64, ref oxews.ItemIDElem) *itemsWrap {
	if msg, err := st.OpenMessage(id); err == nil && isAppointment(itemClass(msg.Props)) {
		return &itemsWrap{CalendarItems: []oxews.CalendarItem{{ItemID: ref}}}
	}
	return &itemsWrap{Messages: []oxews.Message{{ItemID: ref}}}
}
