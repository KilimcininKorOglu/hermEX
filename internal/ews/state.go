package ews

import (
	"encoding/json"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ewsState is the whole mailbox's EWS sync state, persisted as JSON in the
// store-root PrEwsSyncState property. It mirrors the ActiveSync state store: one
// blob per mailbox, no dedicated table.
type ewsState struct {
	HierarchyState   string                      `json:"hierarchyState,omitempty"`
	HierarchyFolders []int64                     `json:"hierarchyFolders,omitempty"`
	Folders          map[string]*folderItemState `json:"folders,omitempty"`
}

// folderItemState is one folder's item sync state for SyncFolderItems: the
// current sync-state token and the item snapshot (opaque ItemId -> flag bits)
// the snapshot-diff compares the live folder against. Change numbers cannot
// drive this (they are INSERT-only — read/flag changes and deletes never bump
// them), so a snapshot diff is the only channel-agnostic way to detect them.
type folderItemState struct {
	SyncState string           `json:"syncState,omitempty"`
	Items     map[string]int64 `json:"items,omitempty"`
}

// folder returns a folder's item sync state, creating it if absent.
func (s *ewsState) folder(key string) *folderItemState {
	if s.Folders == nil {
		s.Folders = map[string]*folderItemState{}
	}
	f := s.Folders[key]
	if f == nil {
		f = &folderItemState{}
		s.Folders[key] = f
	}
	return f
}

// loadState reads the mailbox's EWS state, returning an empty state when no
// client has synced yet.
func loadState(st *objectstore.Store) (*ewsState, error) {
	raw, err := st.GetEwsState()
	if err != nil {
		return nil, err
	}
	s := &ewsState{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// saveState persists the mailbox's EWS state.
func saveState(st *objectstore.Store, s *ewsState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.SetEwsState(string(b))
}

// nextSyncState returns the successor of an opaque integer sync-state token; an
// empty or unparseable token yields "1" (a fresh prime). The token is opaque to
// the client; a stale token after a re-prime is treated as a fresh sync.
func nextSyncState(token string) string {
	n, err := strconv.ParseUint(token, 10, 64)
	if err != nil {
		return "1"
	}
	return strconv.FormatUint(n+1, 10)
}

// distinguishedFolders maps EWS distinguished folder ids to built-in folder ids.
var distinguishedFolders = map[string]int64{
	"msgfolderroot": mapi.PrivateFIDIPMSubtree,
	"root":          mapi.PrivateFIDRoot,
	"inbox":         mapi.PrivateFIDInbox,
	"sentitems":     mapi.PrivateFIDSentItems,
	"deleteditems":  mapi.PrivateFIDDeletedItems,
	"drafts":        mapi.PrivateFIDDraft,
	"outbox":        mapi.PrivateFIDOutbox,
	"junkemail":     mapi.PrivateFIDJunk,
	"calendar":      mapi.PrivateFIDCalendar,
	"contacts":      mapi.PrivateFIDContacts,
	"tasks":         mapi.PrivateFIDTasks,
	"notes":         mapi.PrivateFIDNotes,
	"journal":       mapi.PrivateFIDJournal,
}
