package rop

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf16"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// le16/le32/le64/utf16z build the expected wire bytes by hand — independent of
// pushNotify's own logic — so the test pins the actual byte layout, not a
// re-derivation of the serializer (Rule 11). The expected slices below encode the
// per-type field order and gating of the internal spec §3 field by field.
func le16(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
func le32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }
func le64(v uint64) []byte { b := make([]byte, 8); binary.LittleEndian.PutUint64(b, v); return b }

func utf16z(s string) []byte {
	out := []byte{}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return append(out, 0, 0)
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// TestPushNotify pins the RopNotify wire bytes for one notification of every
// gated shape. Each expected slice is hand-assembled from §3's field order; a
// regression in the serializer's gating (a field written when it should be
// skipped, or in the wrong order) fails the matching case. The opcode byte is
// hardcoded 0x2A rather than referencing ropNotify so the golden bytes do not
// move if the constant is ever mis-edited.
func TestPushNotify(t *testing.T) {
	const handle uint32 = 0x11223344
	const logon uint8 = 0x05
	hdr := cat([]byte{0x2A}, le32(handle), []byte{logon})

	cases := []struct {
		name string
		n    *notification
		want []byte
	}{
		{
			// NewMail: folder_id, message_id, msg_flags, unicode_flag, msg_class (UTF-16LE).
			name: "new_mail",
			n: &notification{
				flags: fnevNewMail | nfByMessage, folderID: 0x0102030405060708,
				messageID: 0x1112131415161718, msgFlags: 0xAB, msgClass: "IPM.Note",
			},
			want: cat(hdr, le16(0x8002), le64(0x0102030405060708), le64(0x1112131415161718),
				le32(0xAB), []byte{0x01}, utf16z("IPM.Note")),
		},
		{
			// message_created: folder_id, message_id, proptags. No parent_id (by-message,
			// not by-search → xnor gate false).
			name: "message_created",
			n: &notification{
				flags: fnevObjectCreated | nfByMessage, folderID: 0x0F0E0D0C0B0A0908,
				messageID: 0x0807060504030201, proptags: []mapi.PropTag{0x12345678, 0x0037001F},
			},
			want: cat(hdr, le16(0x8004), le64(0x0F0E0D0C0B0A0908), le64(0x0807060504030201),
				le16(2), le32(0x12345678), le32(0x0037001F)),
		},
		{
			// folder_created: folder_id, parent_id, proptags. Folder-level → parent_id
			// present (by-search and by-message both clear → xnor gate true).
			name: "folder_created",
			n: &notification{
				flags: fnevObjectCreated, folderID: 0x2222222222222222,
				parentID: 0x3333333333333333, proptags: []mapi.PropTag{0x00010002},
			},
			want: cat(hdr, le16(0x0004), le64(0x2222222222222222), le64(0x3333333333333333),
				le16(1), le32(0x00010002)),
		},
		{
			// folder_modified with total+unread: folder_id, proptags, total, unread.
			// No parent_id — Modified is not in the parent_id gate set.
			name: "folder_modified_counts",
			n: &notification{
				flags:    fnevObjectModified | nfHasTotal | nfHasUnread,
				folderID: 0x4444444444444444, totalCount: 200, unreadCount: 5,
			},
			want: cat(hdr, le16(0x3010), le64(0x4444444444444444), le16(0), le32(200), le32(5)),
		},
		{
			// message_moved: folder_id, message_id, old_folder_id, old_message_id.
			// No parent_id (by-message xnor) and no old_parent_id (by-message set).
			name: "message_moved",
			n: &notification{
				flags: fnevObjectMoved | nfByMessage, folderID: 0x5151515151515151,
				messageID: 0x5252525252525252, oldFolderID: 0x5353535353535353,
				oldMessageID: 0x5454545454545454,
			},
			want: cat(hdr, le16(0x8020), le64(0x5151515151515151), le64(0x5252525252525252),
				le64(0x5353535353535353), le64(0x5454545454545454)),
		},
		{
			// search_completed: folder_id only.
			name: "search_completed",
			n:    &notification{flags: fnevSearchComplete, folderID: 0x6666666666666666},
			want: cat(hdr, le16(0x0080), le64(0x6666666666666666)),
		},
		{
			// content-table ROW_ADDED: row_folder, row_message, row_instance, after_folder,
			// after_row, after_instance, row_data. No trailing folder_id/message_id.
			name: "cttbl_row_added",
			n: &notification{
				flags: fnevTableModified | nfByMessage, tableEvent: tableRowAdded,
				rowFolderID: 0x7171717171717171, rowMessageID: 0x7272727272727272, rowInstance: 0x73,
				afterFolderID: 0x7474747474747474, afterRowID: 0x7575757575757575, afterInstance: 0x76,
				rowData: []byte{0xDE, 0xAD, 0xBE, 0xEF},
			},
			want: cat(hdr, le16(0x8100), le16(0x0003),
				le64(0x7171717171717171), le64(0x7272727272727272), le32(0x73),
				le64(0x7474747474747474), le64(0x7575757575757575), le32(0x76),
				le16(4), []byte{0xDE, 0xAD, 0xBE, 0xEF}),
		},
		{
			// content-table ROW_DELETED: row_folder, row_message, row_instance. No
			// after_* and no row_data (am is false for a delete).
			name: "cttbl_row_deleted",
			n: &notification{
				flags: fnevTableModified | nfByMessage, tableEvent: tableRowDeleted,
				rowFolderID: 0x8181818181818181, rowMessageID: 0x8282828282828282, rowInstance: 0x83,
			},
			want: cat(hdr, le16(0x8100), le16(0x0004),
				le64(0x8181818181818181), le64(0x8282828282828282), le32(0x83)),
		},
		{
			// TABLE_CHANGED: header + table_event only (neither am nor amd).
			name: "table_changed",
			n:    &notification{flags: fnevTableModified, tableEvent: tableChanged},
			want: cat(hdr, le16(0x0100), le16(0x0001)),
		},
		{
			// RESTRICTION_CHANGED: same shape as TABLE_CHANGED.
			name: "table_restriction_changed",
			n:    &notification{flags: fnevTableModified, tableEvent: tableRestrictionChanged},
			want: cat(hdr, le16(0x0100), le16(0x0007)),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ext.NewPush(ext.FlagUTF16)
			if err := pushNotify(out, handle, logon, tc.n); err != nil {
				t.Fatalf("pushNotify: %v", err)
			}
			if got := out.Bytes(); !bytes.Equal(got, tc.want) {
				t.Errorf("wire mismatch\n got %x\nwant %x", got, tc.want)
			}
		})
	}
}
