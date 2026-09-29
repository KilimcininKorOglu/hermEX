package ews

import (
	"encoding/xml"
	"errors"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// noteItem is a base <t:Item> ([MS-OXWSCORE] ItemType) as a client sends a sticky
// note, which EWS has no type of its own for: the fields a note stores. Every other
// element lands in Other and refuses the item, so nothing the client set is dropped
// without a word.
type noteItem struct {
	ItemClass string `xml:"ItemClass"`
	Subject   string `xml:"Subject"`
	Body      struct {
		Type    string `xml:"BodyType,attr"`
		Content string `xml:",chardata"`
	} `xml:"Body"`
	Categories struct {
		String []string `xml:"String"`
	} `xml:"Categories"`
	Importance  string                   `xml:"Importance"`
	Sensitivity string                   `xml:"Sensitivity"`
	Extended    []oxews.ExtendedProperty `xml:"ExtendedProperty"`
	Other       []struct {
		XMLName xml.Name
	} `xml:",any"`
}

// errNoteField reports a note field whose value this server cannot store.
var errNoteField = errors.New("ews: an unsupported note field")

// isNoteClass reports whether a message class is a sticky note's.
func isNoteClass(class string) bool {
	return strings.EqualFold(class, oxews.NoteClass) || strings.HasPrefix(strings.ToUpper(class), strings.ToUpper(oxews.NoteClass)+".")
}

// createNotes stores every base Item in the request as a sticky note, in the
// folder it names or Notes. A base Item of another class is refused: this server
// stores no generic item.
func (s *Server) createNotes(st *objectstore.Store, req createItemRequest) []itemResponseMessage {
	if len(req.Items.Items) == 0 {
		return nil
	}
	fid, code := classSaveFolder(st, req.SavedItemFolderID, int64(mapi.PrivateFIDNotes), mapi.ContainerClassStickyNote)
	msgs := make([]itemResponseMessage, 0, len(req.Items.Items))
	for _, item := range req.Items.Items {
		if code != "" {
			msgs = append(msgs, itemError(code))
			continue
		}
		msgs = append(msgs, createOneNote(st, fid, item))
	}
	return msgs
}

// createOneNote stores one sticky note.
func createOneNote(st *objectstore.Store, fid int64, item noteItem) itemResponseMessage {
	if item.ItemClass != "" && !isNoteClass(item.ItemClass) {
		return itemError("ErrorInvalidPropertySet")
	}
	if len(item.Other) > 0 {
		return itemError("ErrorInvalidPropertySet")
	}
	class := item.ItemClass
	if class == "" {
		class = oxews.NoteClass
	}
	props := mapi.PropertyValues{{Tag: mapi.PrMessageClass, Value: class}}
	for _, field := range []string{"item:Subject", "item:Body", "item:Categories", "item:Importance", "item:Sensitivity"} {
		if err := item.write(st, field, &props); err != nil {
			return itemError("ErrorInvalidPropertySet")
		}
	}
	ext, code := extendedValues(st, item.Extended)
	if code != "" {
		return itemError(code)
	}
	for _, pv := range ext {
		props.Set(pv.Tag, pv.Value)
	}
	id, err := st.CreateMessage(fid, &oxcmail.Message{Props: props})
	if err != nil {
		return itemError("ErrorItemSave")
	}
	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: fid, MessageID: id})
	return itemFound(&itemsWrap{BaseItems: []oxews.Item{{ItemID: oxews.ItemIDElem{ID: itemID, ChangeKey: changeKey(st, id)}}}})
}

// write sets the property one note field names from the item's value.
func (item noteItem) write(st *objectstore.Store, field string, props *mapi.PropertyValues) error {
	switch field {
	case "item:Subject":
		props.Set(mapi.PrSubject, item.Subject)
	case "item:Body":
		if strings.EqualFold(item.Body.Type, "HTML") {
			props.Set(mapi.PrHTML, []byte(oxews.ToCRLF(item.Body.Content)))
		} else {
			props.Set(mapi.PrBody, oxews.ToCRLF(item.Body.Content))
		}
	case "item:Categories":
		return setKeywords(st, props, item.Categories.String)
	case "item:Importance":
		return setNamed(props, mapi.PrImportance, importanceValues, item.Importance)
	case "item:Sensitivity":
		return setNamed(props, mapi.PrSensitivity, sensitivityValues, item.Sensitivity)
	default:
		return errNoteField
	}
	return nil
}

// setKeywords sets the categories (PidNameKeywords), none when cats is empty.
func setKeywords(st *objectstore.Store, props *mapi.PropertyValues, cats []string) error {
	if len(cats) == 0 {
		return nil
	}
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameKeywords})
	if err != nil {
		return err
	}
	props.Set(mapi.MakeTag(ids[0], mapi.PtMvUnicode), cats)
	return nil
}

// setNamed sets a PtLong property from its EWS name, nothing when the name is
// empty, and refuses a name the table does not hold.
func setNamed(props *mapi.PropertyValues, tag mapi.PropTag, values map[string]int32, name string) error {
	if name == "" {
		return nil
	}
	v, ok := values[name]
	if !ok {
		return errNoteField
	}
	props.Set(tag, v)
	return nil
}

// classSaveFolder is the folder a created item of one kind is stored in: the one
// the request names, which must be of the caller's own mailbox and hold the
// container class, else the default folder.
func classSaveFolder(st *objectstore.Store, refs folderRefs, def int64, container string) (int64, string) {
	targets := resolveTargets(refs)
	if len(targets) == 0 {
		return def, ""
	}
	t := targets[0]
	if !t.ok || t.mailbox != "" {
		return 0, "ErrorFolderNotFound"
	}
	if t.fid == def {
		return t.fid, ""
	}
	props, err := st.GetFolderProperties(t.fid, mapi.PrContainerClass)
	if errors.Is(err, objectstore.ErrNotFound) {
		return 0, "ErrorFolderNotFound"
	}
	if err != nil {
		return 0, "ErrorInternalServerError"
	}
	class := strProp(props, mapi.PrContainerClass)
	if class != container && !strings.HasPrefix(class, container+".") {
		return 0, "ErrorInvalidRequest"
	}
	return t.fid, ""
}

// noteClearers are the note fields a DeleteItemField removes, with the tags each
// one takes off the note.
var noteClearers = map[string][]mapi.PropTag{
	"item:Body":        {mapi.PrBody, mapi.PrHTML},
	"item:Importance":  {mapi.PrImportance},
	"item:Sensitivity": {mapi.PrSensitivity},
}

// isNoteItem reports whether an object-store item is a sticky note.
func isNoteItem(st *objectstore.Store, id int64) bool {
	props, err := st.GetMessageProperties(id, mapi.PrMessageClass)
	return err == nil && isNoteClass(strProp(props, mapi.PrMessageClass))
}

// updateNote applies one ItemChange to a stored sticky note in place: the fields
// it names are written, the ones it deletes removed, and the note keeps its id and
// every other property.
func updateNote(st *objectstore.Store, id oxews.ItemID, ch itemChangeReq) itemResponseMessage {
	set, removed, code := noteChange(st, ch)
	if code != "" {
		return itemError(code)
	}
	ext, code := taskExtended(st, ch)
	if code != "" {
		return itemError(code)
	}
	ext.apply(&set)
	removed = append(removed, ext.remove...)
	if err := st.ModifyMessageProperties(id.MessageID, set, removed...); err != nil {
		return itemError("ErrorItemNotFound")
	}
	return itemFound(&itemsWrap{BaseItems: []oxews.Item{{ItemID: oxews.ItemIDElem{ID: ch.ItemID.ID, ChangeKey: changeKey(st, id.MessageID)}}}})
}

// noteChange reads the properties an ItemChange sets and removes on a note. A body
// in one format removes the other, so the note never exports a stale half.
func noteChange(st *objectstore.Store, ch itemChangeReq) (mapi.PropertyValues, []mapi.PropTag, string) {
	var set mapi.PropertyValues
	var removed []mapi.PropTag
	for _, sf := range ch.Updates.SetFields {
		if sf.Extended != nil {
			continue
		}
		if sf.FieldURI.URI == "item:Categories" {
			tags, code := keywordsChange(st, sf.Item.Categories.String, &set)
			if code != "" {
				return nil, nil, code
			}
			removed = append(removed, tags...)
			continue
		}
		if err := sf.Item.write(st, sf.FieldURI.URI, &set); err != nil {
			return nil, nil, "ErrorInvalidPropertySet"
		}
		if sf.FieldURI.URI == "item:Body" {
			removed = append(removed, otherBody(sf.Item.Body.Type))
		}
	}
	cleared, code := noteDeletes(st, ch.Updates.DeleteFields)
	return set, append(removed, cleared...), code
}

// noteDeletes lists the tags the DeleteItemFields of an ItemChange take off a note,
// leaving the extended properties to taskExtended.
func noteDeletes(st *objectstore.Store, fields []deleteItemField) ([]mapi.PropTag, string) {
	var removed []mapi.PropTag
	for _, df := range fields {
		if df.Extended != nil {
			continue
		}
		tags, ok := noteClearers[df.FieldURI.URI]
		if df.FieldURI.URI == "item:Categories" {
			tags, ok = keywordsTag(st), true
		}
		if !ok {
			return nil, "ErrorInvalidPropertyDelete"
		}
		removed = append(removed, tags...)
	}
	return removed, ""
}

// keywordsChange sets the categories, or removes them when the list is empty.
func keywordsChange(st *objectstore.Store, cats []string, set *mapi.PropertyValues) ([]mapi.PropTag, string) {
	if len(cats) == 0 {
		return keywordsTag(st), ""
	}
	if err := setKeywords(st, set, cats); err != nil {
		return nil, "ErrorInternalServerError"
	}
	return nil, ""
}

// keywordsTag is the categories tag of a store that has one, none otherwise.
func keywordsTag(st *objectstore.Store) []mapi.PropTag {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameKeywords})
	if err != nil || ids[0] == 0 {
		return nil
	}
	return []mapi.PropTag{mapi.MakeTag(ids[0], mapi.PtMvUnicode)}
}

// otherBody is the body property a body of the given type replaces.
func otherBody(bodyType string) mapi.PropTag {
	if strings.EqualFold(bodyType, "HTML") {
		return mapi.PrBody
	}
	return mapi.PrHTML
}
