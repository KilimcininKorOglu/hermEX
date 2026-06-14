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
// object store needs — the objdb message id and the idxdb (folder, uid) pair —
// so one id drives OpenMessage (message id), GetMessageRaw/SetMessageFlags/
// DeleteMessage ((folder, uid)), and folder context. EWS clients treat the
// encoded id as opaque, so this layout is private.
type ItemID struct {
	FolderID  int64
	MessageID int64
	UID       uint32
}

// EncodeItemID encodes an item id as an opaque base64 token.
func EncodeItemID(id ItemID) string {
	s := fmt.Sprintf("%d.%d.%d", id.FolderID, id.MessageID, id.UID)
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// DecodeItemID reverses EncodeItemID.
func DecodeItemID(s string) (ItemID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return ItemID{}, errBadID
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 3 {
		return ItemID{}, errBadID
	}
	fid, err1 := strconv.ParseInt(parts[0], 10, 64)
	mid, err2 := strconv.ParseInt(parts[1], 10, 64)
	uid, err3 := strconv.ParseUint(parts[2], 10, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return ItemID{}, errBadID
	}
	return ItemID{FolderID: fid, MessageID: mid, UID: uint32(uid)}, nil
}

// EncodeFolderID encodes a folder id (the objdb folder id) as an opaque token.
func EncodeFolderID(folderID int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(folderID, 10)))
}

// DecodeFolderID reverses EncodeFolderID.
func DecodeFolderID(s string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, errBadID
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, errBadID
	}
	return id, nil
}

// ChangeKey encodes a change number as an opaque EWS change key (a client uses
// it only to detect that an item changed; the value is otherwise opaque).
func ChangeKey(changeNumber uint64) string {
	return strconv.FormatUint(changeNumber, 10)
}
