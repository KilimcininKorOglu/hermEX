package oxews

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errBadID reports a malformed opaque id.
var errBadID = errors.New("oxews: malformed id")

// ItemID is the decoded form of an opaque EWS item id. It carries every key the
// object store needs, the objdb message id and the idxdb (folder, uid) pair,
// so one id drives OpenMessage (message id), GetMessageRaw/SetMessageFlags/
// DeleteMessage ((folder, uid)), and folder context. EWS clients treat the
// encoded id as opaque, so this layout is private.
type ItemID struct {
	FolderID  int64
	MessageID int64
	UID       uint32
	// Mailbox is the target mailbox's SMTP address when the item lives in another
	// mailbox the caller was granted access to; empty for the caller's own mailbox.
	// It rides in the opaque token so a later GetItem/Update/Delete reopens the same
	// mailbox the item was found in. An "|" separates it from the dotted coordinates
	// because an SMTP address itself contains dots.
	Mailbox string
	// Instance names one occurrence of a recurring series: the Unix time of the
	// instant the series generates for it (its RECURRENCE-ID). Zero names the item
	// itself, the series master for a series.
	Instance int64
}

// EncodeItemID encodes an item id as an opaque base64 token. A token from another
// mailbox carries its SMTP after a "|"; an own-mailbox token keeps the original
// three-field form, so ids minted before this field decode unchanged. An
// occurrence id carries its instance as a fourth dotted field.
func EncodeItemID(id ItemID) string {
	s := fmt.Sprintf("%d.%d.%d", id.FolderID, id.MessageID, id.UID)
	if id.Instance != 0 {
		s += "." + strconv.FormatInt(id.Instance, 10)
	}
	if id.Mailbox != "" {
		s += "|" + id.Mailbox
	}
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// ErrOccurrenceID reports an occurrence id where only an item id is accepted.
var ErrOccurrenceID = errors.New("oxews: an occurrence id names no stored item")

// DecodeItemID reverses EncodeItemID for an id that names a stored item. An
// occurrence id is refused with ErrOccurrenceID, so an operation that has not been
// taught occurrences never applies itself to the whole series instead.
func DecodeItemID(s string) (ItemID, error) {
	id, err := DecodeAnyItemID(s)
	if err != nil {
		return ItemID{}, err
	}
	if id.Instance != 0 {
		return ItemID{}, ErrOccurrenceID
	}
	return id, nil
}

// DecodeAnyItemID reverses EncodeItemID, occurrence ids included. A token with no
// "|" segment decodes to an own-mailbox id (empty Mailbox), preserving
// compatibility with older tokens.
func DecodeAnyItemID(s string) (ItemID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return ItemID{}, errBadID
	}
	str := string(raw)
	mailbox := ""
	if i := strings.IndexByte(str, '|'); i >= 0 {
		mailbox = str[i+1:]
		str = str[:i]
	}
	id, err := parseItemCoords(strings.Split(str, "."))
	if err != nil {
		return ItemID{}, err
	}
	id.Mailbox = mailbox
	return id, nil
}

// parseItemCoords reads the dotted fields of an item id: folder, message and uid,
// then the instance of an occurrence id.
func parseItemCoords(parts []string) (ItemID, error) {
	if len(parts) != 3 && len(parts) != 4 {
		return ItemID{}, errBadID
	}
	fid, err1 := strconv.ParseInt(parts[0], 10, 64)
	mid, err2 := strconv.ParseInt(parts[1], 10, 64)
	uid, err3 := strconv.ParseUint(parts[2], 10, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return ItemID{}, errBadID
	}
	id := ItemID{FolderID: fid, MessageID: mid, UID: uint32(uid)}
	if len(parts) == 4 {
		inst, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil || inst == 0 {
			return ItemID{}, errBadID
		}
		id.Instance = inst
	}
	return id, nil
}

// EncodeFolderID encodes an own-mailbox folder id as an opaque token.
func EncodeFolderID(folderID int64) string {
	return encodeFolderID(folderID, "")
}

// EncodeFolderIDFor encodes a folder id in another mailbox, carrying the target SMTP
// so a later request reopens the same mailbox. An empty mailbox yields the own-mailbox
// form, identical to EncodeFolderID.
func EncodeFolderIDFor(folderID int64, mailbox string) string {
	return encodeFolderID(folderID, mailbox)
}

func encodeFolderID(folderID int64, mailbox string) string {
	s := strconv.FormatInt(folderID, 10)
	if mailbox != "" {
		s += "|" + mailbox
	}
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// DecodeFolderID reverses the folder-id encoding, returning the folder id and the
// target mailbox SMTP (empty for the caller's own mailbox). A token with no "|"
// segment decodes to an own-mailbox id, so ids minted before the field decode
// unchanged.
func DecodeFolderID(s string) (folderID int64, mailbox string, err error) {
	raw, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil {
		return 0, "", errBadID
	}
	str := string(raw)
	if i := strings.IndexByte(str, '|'); i >= 0 {
		mailbox = str[i+1:]
		str = str[:i]
	}
	folderID, e = strconv.ParseInt(str, 10, 64)
	if e != nil {
		return 0, "", errBadID
	}
	return folderID, mailbox, nil
}

// ChangeKey encodes a change number as an opaque EWS change key (a client uses
// it only to detect that an item changed; the value is otherwise opaque).
func ChangeKey(changeNumber uint64) string {
	return strconv.FormatUint(changeNumber, 10)
}
