package rop

import (
	"testing"

	"hermex/internal/mapi"
)

// TestSubscriptionMatches pins the [MS-OXCNOTIF] match rule and the scope
// asymmetry it depends on: a whole-store subscription sees every event, a folder
// subscription sees folder-scoped events (a create), and a message subscription
// sees message-scoped events (a modify/delete) — each gated by the type bitmask.
func TestSubscriptionMatches(t *testing.T) {
	const folderF, folderG int64 = 100, 200
	const msgM int64 = 555
	created := uint8(fnevObjectCreated)
	modified := uint8(fnevObjectModified)
	deleted := uint8(fnevObjectDeleted)

	tests := []struct {
		name           string
		sub            subscription
		folderID       int64
		scopeMessageID int64
		typeBit        uint8
		want           bool
	}{
		{"whole-store sees a folder-scoped create", subscription{wholeStore: true, types: 0xFF}, folderF, 0, created, true},
		{"whole-store sees a message-scoped delete", subscription{wholeStore: true, types: 0xFF}, folderG, msgM, deleted, true},
		{"folder sub sees a create in its folder", subscription{types: created, folderID: folderF}, folderF, 0, created, true},
		{"folder sub: wrong type filtered out", subscription{types: created, folderID: folderF}, folderF, 0, modified, false},
		{"folder sub: a message-scoped modify misses it", subscription{types: created | modified, folderID: folderF}, folderF, msgM, modified, false},
		{"folder sub: wrong folder", subscription{types: created, folderID: folderF}, folderG, 0, created, false},
		{"message sub sees a modify of its message", subscription{types: modified | deleted, folderID: folderF, messageID: msgM}, folderF, msgM, modified, true},
		{"message sub: a folder-scoped create misses it", subscription{types: modified | deleted, folderID: folderF, messageID: msgM}, folderF, 0, created, false},
		{"message sub: a different message", subscription{types: deleted, folderID: folderF, messageID: msgM}, folderF, 999, deleted, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sub.matches(tc.folderID, tc.scopeMessageID, tc.typeBit); got != tc.want {
				t.Errorf("matches(%d, %d, %#x) = %v, want %v", tc.folderID, tc.scopeMessageID, tc.typeBit, got, tc.want)
			}
		})
	}
}

// TestClassifyScope proves the per-event scope: a create/new-mail is folder-scoped
// (message id 0), while a modify/delete carries the real message id recovered from
// the notification's wire EID. The type bit is the low byte of the flags.
func TestClassifyScope(t *testing.T) {
	const objMsg int64 = 0x5678
	wireMsg := uint64(mapi.MakeEIDEx(1, uint64(objMsg)))

	tests := []struct {
		name      string
		n         notification
		wantScope int64
		wantType  uint8
	}{
		{"created is folder-scoped", notification{flags: fnevObjectCreated | nfByMessage, messageID: wireMsg}, 0, uint8(fnevObjectCreated)},
		{"new-mail is folder-scoped", notification{flags: fnevNewMail | nfByMessage, messageID: wireMsg}, 0, uint8(fnevNewMail)},
		{"modified is message-scoped", notification{flags: fnevObjectModified | nfByMessage, messageID: wireMsg}, objMsg, uint8(fnevObjectModified)},
		{"deleted is message-scoped", notification{flags: fnevObjectDeleted | nfByMessage, messageID: wireMsg}, objMsg, uint8(fnevObjectDeleted)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scope, typeBit := classifyScope(&tc.n)
			if scope != tc.wantScope || typeBit != tc.wantType {
				t.Errorf("classifyScope = (%d, %#x), want (%d, %#x)", scope, typeBit, tc.wantScope, tc.wantType)
			}
		})
	}
}
