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
}

// objectClasses is the single dispatch source for the object folders.
var objectClasses = map[int64]objectClass{
	int64(mapi.PrivateFIDCalendar): {int64(mapi.PrivateFIDCalendar), calendarAppData, parseCalendarProps},
	int64(mapi.PrivateFIDContacts): {int64(mapi.PrivateFIDContacts), contactAppData, parseContactProps},
	int64(mapi.PrivateFIDTasks):    {int64(mapi.PrivateFIDTasks), taskAppData, parseTaskProps},
	int64(mapi.PrivateFIDNotes):    {int64(mapi.PrivateFIDNotes), noteAppData, parseNoteItem},
}

// isObjectFolder reports whether a folder's items live in the object store and are
// versioned by change number (calendar, contacts, tasks, notes) rather than the
// IMAP index.
func isObjectFolder(folderID int64) bool {
	_, ok := objectClasses[folderID]
	return ok
}

// classOf returns a folder's data class, falling back to the calendar for a
// folder the callers only reach after isObjectFolder said yes.
func classOf(folderID int64) objectClass {
	if cls, ok := objectClasses[folderID]; ok {
		return cls
	}
	return objectClasses[int64(mapi.PrivateFIDCalendar)]
}

// objectAppData returns the data-class renderer for an object folder's items.
func objectAppData(folderID int64) func(*objectstore.Store, int64) (*wbxml.Node, error) {
	return classOf(folderID).appData
}

// applyObjectClientCommands applies a device's Add/Change/Delete commands to an
// object folder and returns the Add responses carrying the assigned server ids.
func applyObjectClientCommands(st *objectstore.Store, folderID int64, cstate *collectionState, c *wbxml.Node) []*wbxml.Node {
	cls := classOf(folderID)
	cmds := c.Child(wbxml.ASCommands)
	if cmds == nil {
		return nil
	}
	var responses []*wbxml.Node
	added := map[string]bool{}
	for _, cmd := range cmds.Children {
		switch cmd.Tag {
		case wbxml.ASAdd:
			if resp, sid := cls.addObject(st, cmd); resp != nil {
				added[sid] = true
				responses = append(responses, resp)
			}
		case wbxml.ASChange:
			cls.changeObject(st, cmd)
		case wbxml.ASDelete:
			deleteObject(st, cstate, cmd)
		}
	}
	foldAddedIntoSnapshot(st, cls.folderID, cstate, added)
	return responses
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

// changeObject rewrites one item's properties from a device's Change command.
func (cls objectClass) changeObject(st *objectstore.Store, cmd *wbxml.Node) {
	id, err := strconv.ParseInt(cmd.ChildText(wbxml.ASServerID), 10, 64)
	if err != nil {
		return
	}
	data := cmd.Child(wbxml.ASData)
	if data == nil {
		return
	}
	props, err := cls.parse(st, data)
	if err != nil || len(props) == 0 {
		return
	}
	_ = st.SetMessageProperties(id, props)
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

// foldAddedIntoSnapshot records the just-added items at their current change
// number, so objectChanges does not echo them back as server adds to the device
// that just created them.
func foldAddedIntoSnapshot(st *objectstore.Store, folderID int64, cstate *collectionState, added map[string]bool) {
	if len(added) == 0 {
		return
	}
	objs, err := st.ListFolderObjects(folderID)
	if err != nil {
		return
	}
	for _, o := range objs {
		if sid := strconv.FormatInt(o.ID, 10); added[sid] {
			// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
			cstate.Items[sid] = int64(o.ChangeNumber)
		}
	}
}
