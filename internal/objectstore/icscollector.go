package objectstore

import (
	"fmt"

	"hermex/internal/ics"
	"hermex/internal/mapi"
)

// UploadCollector is the ICS upload-side synchronization-state collector
// ([MS-OXCFXICS] 3.3.5). One collector spans a single upload: the client first
// replays its prior checkpoint as a sequence of state streams (one per idset
// meta-tag, framed BeginStateStream/ContinueStateStream/EndStateStream), then
// issues imports. GetTransferState serializes the resulting state so the client
// can adopt it as the next checkpoint.
//
// Only the imports that have no per-object save-changes read-back fold their
// server-assigned change numbers into the state: read-state into the read set,
// hierarchy into the seen set. A message change conveys its change number to the
// client through the saved message (PidTagChangeNumber), not through this state,
// so it is not routed through the collector; deletes carry no change number at
// all. The state stream must complete before the first import (the reference's
// mark-started gate).
type UploadCollector struct {
	store     *Store
	folderID  int64
	syncType  uint8
	state     *ics.State
	streamTag uint32
	streamBuf []byte
	started   bool
}

// NewContentUpload opens a contents-side upload collector for folderID, seeded
// with an empty ContentsUp state against the store's replica mapper.
func (s *Store) NewContentUpload(folderID int64) (*UploadCollector, error) {
	m, err := s.ReplicaMapper()
	if err != nil {
		return nil, err
	}
	return &UploadCollector{
		store:    s,
		folderID: folderID,
		syncType: SyncTypeContents,
		state:    ics.NewState(ics.ContentsUp, m),
	}, nil
}

// NewHierarchyUpload opens a hierarchy-side upload collector rooted at rootFID,
// seeded with an empty HierarchyUp state.
func (s *Store) NewHierarchyUpload(rootFID int64) (*UploadCollector, error) {
	m, err := s.ReplicaMapper()
	if err != nil {
		return nil, err
	}
	return &UploadCollector{
		store:    s,
		folderID: rootFID,
		syncType: SyncTypeHierarchy,
		state:    ics.NewState(ics.HierarchyUp, m),
	}, nil
}

// State returns the collector's accumulated synchronization state.
func (c *UploadCollector) State() *ics.State { return c.state }

// BeginStateStream opens an idset state stream under metaTag. The whole state must
// be replayed before the first import (the mark-started gate). A stream opened
// after an import, a non-state meta-tag, a contents-only set (cnset-seen-fai /
// cnset-read) on a hierarchy upload, or a second stream opened while one is still
// open is rejected. A given set is accepted for every sync type but, per the
// protocol, retained by none (see ContinueStateStream).
func (c *UploadCollector) BeginStateStream(metaTag uint32) error {
	if c.started {
		return fmt.Errorf("objectstore: state stream opened after an import")
	}
	if c.streamTag != 0 {
		return fmt.Errorf("objectstore: state stream %#x already open", c.streamTag)
	}
	if !ics.IsStateMetaTag(metaTag) {
		return fmt.Errorf("objectstore: %#x is not a state meta-tag", metaTag)
	}
	if c.syncType != SyncTypeContents && ics.IsContentsOnlyStateMetaTag(metaTag) {
		return fmt.Errorf("objectstore: %#x is a contents-only state on a hierarchy upload", metaTag)
	}
	c.streamTag = metaTag
	c.streamBuf = nil
	return nil
}

// ContinueStateStream appends a chunk to the open state stream. A given stream is
// accepted but its bytes are dropped: an importing context keeps no record of the
// ids the client already holds, so the given set is never reconstructed.
func (c *UploadCollector) ContinueStateStream(data []byte) error {
	if c.started {
		return fmt.Errorf("objectstore: state stream continued after an import")
	}
	if c.streamTag == 0 {
		return fmt.Errorf("objectstore: no open state stream")
	}
	if ics.IsGivenStateMetaTag(c.streamTag) {
		return nil
	}
	c.streamBuf = append(c.streamBuf, data...)
	return nil
}

// EndStateStream folds the buffered idset into the collector state under the open
// meta-tag and closes the stream. A given stream closes without folding anything
// in (its bytes were dropped); an empty buffer for any other set yields an empty
// idset (an initial-sync upload), not an error.
func (c *UploadCollector) EndStateStream() error {
	if c.started {
		return fmt.Errorf("objectstore: state stream ended after an import")
	}
	if c.streamTag == 0 {
		return fmt.Errorf("objectstore: no open state stream to end")
	}
	tag := c.streamTag
	buf := c.streamBuf
	c.streamTag = 0
	c.streamBuf = nil
	if ics.IsGivenStateMetaTag(tag) {
		return nil
	}
	return c.state.AppendIDSet(tag, buf)
}

// ImportReadStateChanges applies read-flag changes through the store and folds the
// resulting read change numbers into the collector's read set ([MS-OXCFXICS]
// 3.3.5.5).
func (c *UploadCollector) ImportReadStateChanges(changes []ReadStateChange) error {
	if c.syncType != SyncTypeContents {
		return fmt.Errorf("objectstore: read-state import requires a contents collector")
	}
	c.started = true
	readCNs, err := c.store.ImportReadStateChanges(c.folderID, changes)
	if err != nil {
		return err
	}
	for _, cn := range readCNs {
		c.state.Read().AppendRange(homeReplID, cn, cn)
	}
	return nil
}

// ImportHierarchyChange creates or updates a folder through the store and folds its
// change number into the collector's seen set ([MS-OXCFXICS] 3.3.5.4). The folder's
// change number is read back from the store rather than threaded through the import
// return, leaving the committed hierarchy-import signature untouched.
func (c *UploadCollector) ImportHierarchyChange(hichyvals, propvals mapi.PropertyValues) (uint64, error) {
	if c.syncType != SyncTypeHierarchy {
		return 0, fmt.Errorf("objectstore: hierarchy import requires a hierarchy collector")
	}
	c.started = true
	fid, err := c.store.ImportHierarchyChange(c.folderID, hichyvals, propvals)
	if err != nil {
		return 0, err
	}
	cn, err := c.store.folderChangeNumber(fid)
	if err != nil {
		return 0, err
	}
	c.state.Seen().AppendRange(homeReplID, cn, cn)
	return fid, nil
}

// ImportDeletes hard-deletes the messages named by their home source keys through
// the store. Deletes carry no change number, so nothing is folded into the state;
// the import only marks the collector started ([MS-OXCFXICS] 3.3.5.6).
func (c *UploadCollector) ImportDeletes(sourceKeys [][]byte) ([]uint64, error) {
	c.started = true
	return c.store.ImportDeletes(c.folderID, sourceKeys)
}

// GetTransferState renders the collected synchronization state as a FastTransfer
// stream the client reads back to adopt as its next checkpoint: INCRSYNCSTATEBEGIN,
// one property per populated idset, INCRSYNCSTATEEND ([MS-OXCFXICS] 3.2.5.5). It
// carries no INCRSYNCEND — a transfer state is a bare state block, not a sync flow.
func (c *UploadCollector) GetTransferState() ([]byte, error) {
	props, err := c.state.Serialize()
	if err != nil {
		return nil, err
	}
	pr := &ics.Producer{}
	pr.WriteMarker(ics.MarkerIncrSyncStateBegin)
	for _, sp := range props {
		if err := pr.WriteProp(sp); err != nil {
			return nil, fmt.Errorf("objectstore: write transfer state %s: %w", sp.Tag, err)
		}
	}
	pr.WriteMarker(ics.MarkerIncrSyncStateEnd)
	var out []byte
	for {
		chunk, last := pr.ReadBuffer(1 << 16)
		out = append(out, chunk...)
		if last {
			break
		}
	}
	return out, nil
}

// folderChangeNumber reads a folder's current change number from the MAPI store.
func (s *Store) folderChangeNumber(fid uint64) (uint64, error) {
	var cn int64
	if err := s.objdb.QueryRow(`SELECT change_number FROM folders WHERE folder_id=?`, int64(fid)).Scan(&cn); err != nil {
		return 0, fmt.Errorf("objectstore: read folder %d change number: %w", fid, err)
	}
	return uint64(cn), nil
}
