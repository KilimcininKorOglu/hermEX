package objectstore

import (
	"fmt"

	"hermex/internal/ics"
	"hermex/internal/mapi"
)

// DownloadCollector is the ICS download-side sync context ([MS-OXCFXICS] 3.3.5).
// SynchronizationConfigure opens it with the requested sync type, flags, and
// property filter; the client then replays its prior checkpoint as state streams;
// the first GetBuffer call lazily computes the delta and streams it. Unlike the
// upload collector, the download RETAINS the client's given set — it is the basis
// for the deletions report — so its state stream buffers every idset, including
// given.
//
// The DownloadContext is built on the first GetBuffer rather than at configure
// time so the whole prior state (uploaded between configure and the first buffer
// fetch) is in hand before the delta is computed.
type DownloadCollector struct {
	store      *Store
	folderID   int64
	syncType   uint8
	syncFlags  uint16
	extraFlags uint32
	propTags   []mapi.PropTag
	state      *ics.State
	streamTag  uint32
	streamBuf  []byte
	dc         *DownloadContext // nil until the first GetBuffer
}

// NewContentDownloadCollector opens a contents-side download context for folderID
// with an empty ContentsDown state for the client to populate.
func (s *Store) NewContentDownloadCollector(folderID int64, syncFlags uint16, extraFlags uint32, propTags []mapi.PropTag) (*DownloadCollector, error) {
	m, err := s.ReplicaMapper()
	if err != nil {
		return nil, err
	}
	return &DownloadCollector{
		store:      s,
		folderID:   folderID,
		syncType:   SyncTypeContents,
		syncFlags:  syncFlags,
		extraFlags: extraFlags,
		propTags:   propTags,
		state:      ics.NewState(ics.ContentsDown, m),
	}, nil
}

// NewHierarchyDownloadCollector opens a hierarchy-side download context rooted at
// rootFID with an empty HierarchyDown state.
func (s *Store) NewHierarchyDownloadCollector(rootFID int64, syncFlags uint16, propTags []mapi.PropTag) (*DownloadCollector, error) {
	m, err := s.ReplicaMapper()
	if err != nil {
		return nil, err
	}
	return &DownloadCollector{
		store:     s,
		folderID:  rootFID,
		syncType:  SyncTypeHierarchy,
		syncFlags: syncFlags,
		propTags:  propTags,
		state:     ics.NewState(ics.HierarchyDown, m),
	}, nil
}

// BeginStateStream opens an idset state stream under metaTag. The state must be
// replayed before the first GetBuffer: a stream opened after the download started,
// a non-state meta-tag, a contents-only set on a hierarchy download, or a second
// stream opened while one is still open is rejected.
func (c *DownloadCollector) BeginStateStream(metaTag uint32) error {
	if c.dc != nil {
		return fmt.Errorf("objectstore: state stream opened after the download started")
	}
	if c.streamTag != 0 {
		return fmt.Errorf("objectstore: state stream %#x already open", c.streamTag)
	}
	if !ics.IsStateMetaTag(metaTag) {
		return fmt.Errorf("objectstore: %#x is not a state meta-tag", metaTag)
	}
	if c.syncType != SyncTypeContents && ics.IsContentsOnlyStateMetaTag(metaTag) {
		return fmt.Errorf("objectstore: %#x is a contents-only state on a hierarchy download", metaTag)
	}
	c.streamTag = metaTag
	c.streamBuf = nil
	return nil
}

// ContinueStateStream appends a chunk to the open state stream. The download keeps
// every idset, including given.
func (c *DownloadCollector) ContinueStateStream(data []byte) error {
	if c.dc != nil {
		return fmt.Errorf("objectstore: state stream continued after the download started")
	}
	if c.streamTag == 0 {
		return fmt.Errorf("objectstore: no open state stream")
	}
	c.streamBuf = append(c.streamBuf, data...)
	return nil
}

// EndStateStream folds the buffered idset into the download state under the open
// meta-tag. An empty buffer yields an empty idset (an initial sync), not an error.
func (c *DownloadCollector) EndStateStream() error {
	if c.dc != nil {
		return fmt.Errorf("objectstore: state stream ended after the download started")
	}
	if c.streamTag == 0 {
		return fmt.Errorf("objectstore: no open state stream to end")
	}
	tag := c.streamTag
	buf := c.streamBuf
	c.streamTag = 0
	c.streamBuf = nil
	return c.state.AppendIDSet(tag, buf)
}

// GetBuffer streams the next chunk of the download. The first call builds the
// DownloadContext from the configured parameters and the now-complete client
// state; subsequent calls drain it. Its signature matches the download context so
// both satisfy the dispatch layer's FastTransfer source.
func (c *DownloadCollector) GetBuffer(maxLen int) (chunk []byte, last bool, err error) {
	if c.dc == nil {
		if c.syncType == SyncTypeHierarchy {
			c.dc, err = c.store.NewHierarchyDownload(c.folderID, c.state, c.syncFlags, c.propTags)
		} else {
			c.dc, err = c.store.NewContentDownload(c.folderID, c.state, c.syncFlags, c.extraFlags, c.propTags)
		}
		if err != nil {
			return nil, false, err
		}
	}
	return c.dc.GetBuffer(maxLen)
}
