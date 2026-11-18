package activesync

import (
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/wbxml"
)

// The four object folders a device may write (calendar, contacts, tasks, notes)
// hold their items in the object store rather than the IMAP index, so they all
// take the same Add/Change/Delete path: an Add creates the item and answers with
// the server id it was assigned, a Change rewrites its properties without
// bumping the change number (so the change is not echoed back to the device that
// made it), and a Delete soft-deletes it. Only the folder, the ApplicationData
// renderer and the ApplicationData parser differ per class, so each class
// contributes those three and shares everything else.

// objectClass is one object folder's data class.
type objectClass struct {
	folderID int64
	appData  func(*objectstore.Store, int64) (*wbxml.Node, error)
	parse    func(*objectstore.Store, *wbxml.Node) (mapi.PropertyValues, error)
	// base is the built-in folder of the class, which names the class itself:
	// two folders hold the same kind of item exactly when their bases match.
	base int64
}

// objectClasses is the single dispatch source for the object folders.
var objectClasses = map[int64]objectClass{
	int64(mapi.PrivateFIDCalendar): {int64(mapi.PrivateFIDCalendar), calendarAppData, parseCalendarProps, int64(mapi.PrivateFIDCalendar)},
	int64(mapi.PrivateFIDContacts): {int64(mapi.PrivateFIDContacts), contactAppData, parseContactProps, int64(mapi.PrivateFIDContacts)},
	int64(mapi.PrivateFIDTasks):    {int64(mapi.PrivateFIDTasks), taskAppData, parseTaskProps, int64(mapi.PrivateFIDTasks)},
	int64(mapi.PrivateFIDNotes):    {int64(mapi.PrivateFIDNotes), noteAppData, parseNoteItem, int64(mapi.PrivateFIDNotes)},
}

// classByContainer gives a user-created object folder the data class of the
// built-in folder of its container class.
var classByContainer = map[string]int64{
	mapi.ContainerClassAppointment: int64(mapi.PrivateFIDCalendar),
	mapi.ContainerClassContact:     int64(mapi.PrivateFIDContacts),
	mapi.ContainerClassTask:        int64(mapi.PrivateFIDTasks),
	mapi.ContainerClassStickyNote:  int64(mapi.PrivateFIDNotes),
}

// objectClassFor reports whether a folder's items live in the object store and
// are versioned by change number (calendar, contacts, tasks, notes) rather than
// the IMAP index, and returns its data class bound to that folder. A built-in
// folder is known by its id, a user-created one by its container class.
func objectClassFor(st *objectstore.Store, folderID int64) (objectClass, bool, error) {
	if cls, ok := objectClasses[folderID]; ok {
		return cls, true, nil
	}
	if _, builtin := wellKnownFolderTypes[folderID]; builtin {
		return objectClass{}, false, nil
	}
	class, err := folderClass(st, folderID)
	if err != nil {
		return objectClass{}, false, err
	}
	base, ok := classByContainer[class]
	if !ok {
		return objectClass{}, false, nil
	}
	cls := objectClasses[base]
	cls.folderID = folderID
	return cls, true, nil
}

// applyObjectClientCommands applies a device's Add/Change/Delete commands to an
// object folder and returns the Add responses carrying the assigned server ids.
func applyObjectClientCommands(st *objectstore.Store, cls objectClass, cstate *collectionState, c *wbxml.Node) []*wbxml.Node {
	folderID := cls.folderID
	cmds := c.Child(wbxml.ASCommands)
	if cmds == nil {
		return nil
	}
	var responses []*wbxml.Node
	written := map[string]bool{}
	for _, cmd := range cmds.Children {
		if refusal := refuseOutsideCollection(st, folderID, cmd); refusal != nil {
			responses = append(responses, refusal)
			continue
		}
		switch cmd.Tag {
		case wbxml.ASAdd:
			if resp, sid := cls.addObject(st, cmd); resp != nil {
				written[sid] = true
				responses = append(responses, resp)
			}
		case wbxml.ASChange:
			if sid := cls.changeObject(st, cmd); sid != "" {
				written[sid] = true
			}
		case wbxml.ASDelete:
			deleteObject(st, cstate, cmd)
		}
	}
	foldClientWritesIntoSnapshot(st, cls.folderID, cstate, written)
	return responses
}

// refuseOutsideCollection answers a Change or Delete whose ServerId names no item
// of the collection with Sync Status 8, MS-ASCMD's "Object not found", and returns
// nil for every command it lets through. The ServerId comes from the device, so a
// store-wide message id is checked against the folder the collection names before
// anything is written: without it a Delete sent for the Calendar could remove a
// task or a mail message.
func refuseOutsideCollection(st *objectstore.Store, folderID int64, cmd *wbxml.Node) *wbxml.Node {
	if cmd.Tag != wbxml.ASChange && cmd.Tag != wbxml.ASDelete {
		return nil
	}
	sid := cmd.ChildText(wbxml.ASServerID)
	if id, err := strconv.ParseInt(sid, 10, 64); err == nil {
		if fid, err := st.MessageFolder(id); err == nil && fid == folderID {
			return nil
		}
	}
	return wbxml.Elem(cmd.Tag,
		wbxml.Str(wbxml.ASServerID, sid),
		wbxml.Str(wbxml.ASStatus, strconv.Itoa(syncStatusObjectNotFound)))
}

// addObject creates one item from a device's Add command and builds the response
// mapping the client id to the server id. It answers a nil node when the command
// is incomplete or the item cannot be stored.
func (cls objectClass) addObject(st *objectstore.Store, cmd *wbxml.Node) (*wbxml.Node, string) {
	clientID := cmd.ChildText(wbxml.ASClientID)
	data := cmd.Child(wbxml.ASData)
	if clientID == "" || data == nil {
		return nil, ""
	}
	props, err := cls.parse(st, data)
	if err != nil {
		return nil, ""
	}
	id, err := st.CreateMessage(cls.folderID, &oxcmail.Message{Props: props})
	if err != nil {
		return nil, ""
	}
	sid := strconv.FormatInt(id, 10)
	return wbxml.Elem(wbxml.ASAdd,
		wbxml.Str(wbxml.ASClientID, clientID),
		wbxml.Str(wbxml.ASServerID, sid),
		wbxml.Str(wbxml.ASStatus, strconv.Itoa(syncStatusOK))), sid
}

// changeObject rewrites one item's properties from a device's Change command and
// returns the server id it wrote, so the caller can fold the edit into the device
// snapshot. It answers an empty id when the command is incomplete or the write fails.
func (cls objectClass) changeObject(st *objectstore.Store, cmd *wbxml.Node) string {
	sid := cmd.ChildText(wbxml.ASServerID)
	id, err := strconv.ParseInt(sid, 10, 64)
	if err != nil {
		return ""
	}
	data := cmd.Child(wbxml.ASData)
	if data == nil {
		return ""
	}
	props, err := cls.parse(st, data)
	if err != nil || len(props) == 0 {
		return ""
	}
	if st.SetMessageProperties(id, props) != nil {
		return ""
	}
	return sid
}

// deleteObject soft-deletes one item and drops it from the device snapshot.
func deleteObject(st *objectstore.Store, cstate *collectionState, cmd *wbxml.Node) {
	sid := cmd.ChildText(wbxml.ASServerID)
	id, err := strconv.ParseInt(sid, 10, 64)
	if err != nil {
		return
	}
	if st.SoftDeleteObject(id) == nil {
		delete(cstate.Items, sid)
	}
}

// foldClientWritesIntoSnapshot records the items the device just added or changed
// at their current change number, so objectChanges does not echo them back to the
// device that wrote them. A Change has to be folded for the same reason an Add
// does: the write advances the item's change number, and without this the device
// receives its own edit back on the next Sync.
func foldClientWritesIntoSnapshot(st *objectstore.Store, folderID int64, cstate *collectionState, written map[string]bool) {
	if len(written) == 0 {
		return
	}
	objs, err := st.ListFolderObjects(folderID)
	if err != nil {
		return
	}
	for _, o := range objs {
		if sid := strconv.FormatInt(o.ID, 10); written[sid] {
			// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
			cstate.Items[sid] = int64(o.ChangeNumber)
		}
	}
}
