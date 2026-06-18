package objectstore

import (
	"database/sql"
	"fmt"
	"time"

	"hermex/internal/ics"
	"hermex/internal/mapi"
)

// SynchronizationFlags ([MS-OXCFXICS] 2.2.3.2.1.1.1). A download context passes
// the seen/seenFAI/read idsets to the delta engine only for the classes these
// flags enable, filters the proptag list as inclusion vs exclusion, and gates
// deletion/read-state reporting.
const (
	SyncUnicode            uint16 = 0x0001
	SyncNoDeletions        uint16 = 0x0002
	SyncNoSoftDeletions    uint16 = 0x0004
	SyncReadState          uint16 = 0x0008
	SyncAssociated         uint16 = 0x0010
	SyncNormal             uint16 = 0x0020
	SyncOnlySpecifiedProps uint16 = 0x0080
	SyncNoForeignKeys      uint16 = 0x0100
	SyncProgressMode       uint16 = 0x8000
)

// SynchronizationExtraFlags ([MS-OXCFXICS] 2.2.3.2.1.1.1): which identity
// properties to add to a message change header. v1 honors EID/MESSAGESIZE/CN;
// ORDERBYDELIVERYTIME (changed-set ordering) is a documented deferral.
const (
	SyncExtraFlagEID                 uint32 = 0x0001
	SyncExtraFlagMessageSize         uint32 = 0x0002
	SyncExtraFlagCN                  uint32 = 0x0004
	SyncExtraFlagOrderByDeliveryTime uint32 = 0x0008
)

// Sync types ([MS-OXCFXICS] 2.2.3.2.1.1.1).
const (
	SyncTypeContents  uint8 = 1
	SyncTypeHierarchy uint8 = 2
)

// storeMapper is the ics replica mapper for one store: the home replica id (1)
// binds to the store's replica GUID. Foreign replicas are not registered until
// the upload path's register_mapping (a documented v1 limitation), so a client
// idset referencing a foreign replica GUID does not resolve yet.
type storeMapper struct{ home mapi.GUID }

func (m storeMapper) ToGUID(replid uint16) (mapi.GUID, bool) {
	if replid == homeReplID {
		return m.home, true
	}
	return mapi.GUID{}, false
}

func (m storeMapper) ToID(g mapi.GUID) (uint16, bool) {
	if g == m.home {
		return homeReplID, true
	}
	return 0, false
}

// ReplicaMapper returns the store's ics replica mapper, which the ROP layer uses
// to convert a client's uploaded (GUID-keyed) state into the queryable form the
// delta engine and download context work in.
func (s *Store) ReplicaMapper() (ics.ReplicaMapper, error) {
	g, err := s.replicaGUID()
	if err != nil {
		return nil, err
	}
	return storeMapper{home: g}, nil
}

// sourceKey builds a PR_SOURCE_KEY: the store replica GUID (flat) followed by the
// 6-byte global counter of the id. It is opaque to the client, which stores and
// echoes it; the only requirements are uniqueness per object and stability,
// which the message/folder id provides.
func sourceKey(replica mapi.GUID, value uint64) []byte {
	f := replica.Flat()
	gc := mapi.ValueToGC(value)
	out := make([]byte, 0, len(f)+len(gc))
	out = append(out, f[:]...)
	return append(out, gc[:]...)
}

// flowKind selects what a flow node emits into the stream when drained.
type flowKind uint8

const (
	flowMessage   flowKind = iota // one changed message (INCRSYNCCHG …)
	flowDeletions                 // INCRSYNCDEL + deleted/no-longer idsets
	flowReadState                 // INCRSYNCREAD + read/unread idsets
	flowState                     // INCRSYNCSTATEBEGIN + new high-water state + END
	flowEnd                       // INCRSYNCEND
)

type flowNode struct {
	kind    flowKind
	mid     uint64 // flowMessage: the message id
	updated bool   // flowMessage: a modification the client already had, vs a new message
}

// DownloadContext produces a contents-synchronization FastTransfer stream for one
// folder: it holds the computed delta, the new high-water state to hand the
// client, and a flow list it drains through an ics producer one element at a time
// across GetBuffer calls. It is created from a client's prior state and consumed
// by the ROP FastTransferSourceGetBuffer handler; it can also be driven directly
// and verified by parsing the stream back.
//
// v1 scope (documented deferrals, matching the slice plan): SYNC_PROGRESS_MODE,
// restriction filtering, and delivery-time ordering are not honored; the changed
// set is emitted in MID order. The read-state and modification ("updated")
// branches are live: the store bumps change_number (ModifyMessageProperties) and
// records read_cn (read.go, ImportReadStateChanges), so an in-place edit or a
// read-flag flip reaches the client.
type DownloadContext struct {
	store      *Store
	producer   *ics.Producer
	mapper     ics.ReplicaMapper
	replica    mapi.GUID
	syncType   uint8
	rootFID    int64 // hierarchy: the configured folder whose subtree is synced
	syncFlags  uint16
	extraFlags uint32
	proptags   map[mapi.PropTag]struct{} // the SyncConfigure property filter
	flow       []flowNode
	flowPos    int

	givenKeep    []uint64
	deletedMIDs  []uint64
	nolongerMIDs []uint64
	readMIDs     []uint64
	unreadMIDs   []uint64
	lastCN       uint64
	lastReadCN   uint64
}

// NewContentDownload computes the contents delta for a folder against the
// client's prior state and records the flow list ([MS-OXCFXICS] 3.3.5.13). state
// holds the client's uploaded given/seen/seenFAI/read idsets (loose, home-keyed);
// proptags is the SyncConfigure property filter (an inclusion list when
// SYNC_ONLY_SPECIFIED_PROPS is set, else an exclusion list).
func (s *Store) NewContentDownload(folderID int64, state *ics.State, syncFlags uint16, extraFlags uint32, proptags []mapi.PropTag) (*DownloadContext, error) {
	replica, err := s.replicaGUID()
	if err != nil {
		return nil, err
	}
	req := ContentSyncRequest{FolderID: folderID, Given: state.Given()}
	if syncFlags&SyncNormal != 0 {
		req.Seen = state.Seen()
	}
	if syncFlags&SyncAssociated != 0 {
		req.SeenFAI = state.SeenFAI()
	}
	if syncFlags&SyncReadState != 0 {
		req.Read = state.Read()
	}
	res, err := s.GetContentSync(req)
	if err != nil {
		return nil, err
	}

	dc := &DownloadContext{
		store:        s,
		producer:     &ics.Producer{},
		mapper:       storeMapper{home: replica},
		replica:      replica,
		syncType:     SyncTypeContents,
		syncFlags:    syncFlags,
		extraFlags:   extraFlags,
		proptags:     make(map[mapi.PropTag]struct{}, len(proptags)),
		givenKeep:    res.GivenMIDs,
		deletedMIDs:  res.DeletedMIDs,
		nolongerMIDs: res.NoLongerMIDs,
		readMIDs:     res.ReadMIDs,
		unreadMIDs:   res.UnreadMIDs,
		lastCN:       res.LastCN,
		lastReadCN:   res.LastReadCN,
	}
	for _, t := range proptags {
		dc.proptags[t] = struct{}{}
	}

	updated := make(map[uint64]struct{}, len(res.UpdatedMIDs))
	for _, m := range res.UpdatedMIDs {
		updated[m] = struct{}{}
	}
	if syncFlags&(SyncAssociated|SyncNormal) != 0 {
		for _, mid := range res.ChangedMIDs {
			_, upd := updated[mid]
			dc.flow = append(dc.flow, flowNode{kind: flowMessage, mid: mid, updated: upd})
		}
	}
	if syncFlags&SyncNoDeletions == 0 {
		dc.flow = append(dc.flow, flowNode{kind: flowDeletions})
	}
	if syncFlags&SyncReadState != 0 {
		dc.flow = append(dc.flow, flowNode{kind: flowReadState})
	}
	dc.flow = append(dc.flow, flowNode{kind: flowState}, flowNode{kind: flowEnd})
	return dc, nil
}

// folderChangeOmit are server-internal/computed folder properties never sent in
// a hierarchy change ([MS-OXCFXICS] 3.3.5.12; the reference strips these). The
// counters and sizes are recomputed by the receiver; the rest of the bag (change
// key, PCL, display name, container class, timestamps, hidden flag) travels.
var folderChangeOmit = map[mapi.PropTag]struct{}{
	mapi.PrDeletedFolderCount:        {},
	mapi.PrInternetArticleNumberNext: {},
	mapi.PrMessageSizeExtended:       {},
	mapi.PrNormalMessageSizeExtended: {},
	mapi.PrAssocMessageSizeExtended:  {},
}

// NewHierarchyDownload computes the subfolder delta for a folder subtree against
// the client's prior state and builds the whole hierarchy stream eagerly — folder
// changes, then deletions, then the new high-water state, then INCRSYNCEND
// ([MS-OXCFXICS] 3.3.5.12). A hierarchy is small, so unlike contents it is not
// drained through a flow list; GetBuffer simply chunks the produced bytes.
//
// v1 emits each folder's stored bag (minus server-internal tags and named
// properties) plus the computed source/parent-source keys; the root folder's
// Outlook special-folder entryid set (PR_IPM_*_ENTRYID, PR_FREEBUSY_ENTRYIDS)
// and the PidTagFolderId/ParentFolderId pair are deferred refinements.
func (s *Store) NewHierarchyDownload(folderID int64, state *ics.State, syncFlags uint16, proptags []mapi.PropTag) (*DownloadContext, error) {
	replica, err := s.replicaGUID()
	if err != nil {
		return nil, err
	}
	res, err := s.GetHierarchySync(HierarchySyncRequest{FolderID: folderID, Given: state.Given(), Seen: state.Seen()})
	if err != nil {
		return nil, err
	}
	dc := &DownloadContext{
		store:       s,
		producer:    &ics.Producer{},
		mapper:      storeMapper{home: replica},
		replica:     replica,
		syncType:    SyncTypeHierarchy,
		rootFID:     folderID,
		syncFlags:   syncFlags,
		proptags:    make(map[mapi.PropTag]struct{}, len(proptags)),
		givenKeep:   res.GivenFIDs,
		deletedMIDs: res.DeletedFIDs,
		lastCN:      res.LastCN,
	}
	for _, t := range proptags {
		dc.proptags[t] = struct{}{}
	}
	for _, fid := range res.ChangedFIDs {
		if err := dc.writeFolderChange(fid); err != nil {
			return nil, err
		}
	}
	if syncFlags&SyncNoDeletions == 0 {
		if err := dc.writeDeletions(); err != nil {
			return nil, err
		}
	}
	if err := dc.writeState(); err != nil {
		return nil, err
	}
	dc.producer.WriteMarker(ics.MarkerIncrSyncEnd)
	return dc, nil
}

// writeFolderChange emits one folder as INCRSYNCCHG + its property bag: the
// stored bag minus server-internal tags and named properties, plus the computed
// source key and parent source key (empty when the parent is the sync root).
func (dc *DownloadContext) writeFolderChange(fid uint64) error {
	props, err := dc.store.GetFolderProperties(int64(fid))
	if err != nil {
		return err
	}
	var parent sql.NullInt64
	if err := dc.store.objdb.QueryRow(
		`SELECT parent_id FROM folders WHERE folder_id=? AND is_deleted=0`, int64(fid)).Scan(&parent); err != nil {
		return err
	}
	out := make(mapi.PropertyValues, 0, len(props)+2)
	for _, p := range props {
		if _, omit := folderChangeOmit[p.Tag]; omit {
			continue
		}
		if uint64(uint16(uint32(p.Tag)>>16)) >= namedPropBase {
			continue // named props are stripped from folder changes
		}
		if p.Tag == mapi.PrSourceKey || p.Tag == mapi.PrParentSourceKey {
			continue // recomputed below
		}
		out = append(out, p)
	}
	out = append(out, mapi.TaggedPropVal{Tag: mapi.PrSourceKey, Value: sourceKey(dc.replica, fid)})
	psk := []byte{}
	if parent.Valid && parent.Int64 != dc.rootFID {
		psk = sourceKey(dc.replica, uint64(parent.Int64))
	}
	out = append(out, mapi.TaggedPropVal{Tag: mapi.PrParentSourceKey, Value: psk})

	out = dc.filter(out)
	dc.producer.WriteMarker(ics.MarkerIncrSyncChg)
	return dc.writeProps(out)
}

// GetBuffer serves up to maxLen bytes of the synchronization stream, feeding flow
// nodes into the producer only as needed to fill the chunk so the whole mailbox
// is never buffered at once. last reports the stream is fully drained.
func (dc *DownloadContext) GetBuffer(maxLen int) (chunk []byte, last bool, err error) {
	for dc.flowPos < len(dc.flow) && dc.producer.PendingLen() < maxLen {
		if err := dc.emitNode(dc.flow[dc.flowPos]); err != nil {
			return nil, false, err
		}
		dc.flowPos++
	}
	chunk, drained := dc.producer.ReadBuffer(maxLen)
	return chunk, drained && dc.flowPos >= len(dc.flow), nil
}

func (dc *DownloadContext) emitNode(n flowNode) error {
	switch n.kind {
	case flowMessage:
		return dc.writeMessageChange(n.mid)
	case flowDeletions:
		return dc.writeDeletions()
	case flowReadState:
		return dc.writeReadState()
	case flowState:
		return dc.writeState()
	case flowEnd:
		dc.producer.WriteMarker(ics.MarkerIncrSyncEnd)
		return nil
	}
	return fmt.Errorf("objectstore: unknown sync flow node %d", n.kind)
}

// writeMessageChange emits one message as INCRSYNCCHG + change header +
// INCRSYNCMESSAGE + body properties + recipients + attachments. The change
// header's source/change keys and PCL are computed from the message id and
// change number (the store does not persist them); PR_LAST_MODIFICATION_TIME
// falls back to now when the message carries none.
func (dc *DownloadContext) writeMessageChange(mid uint64) error {
	var (
		cn, size int64
		assoc    sql.NullInt64
	)
	err := dc.store.objdb.QueryRow(
		`SELECT change_number, is_associated, message_size FROM messages WHERE message_id=? AND is_deleted=0`,
		int64(mid)).Scan(&cn, &assoc, &size)
	if err == sql.ErrNoRows {
		// The message vanished between the delta scan and now; the client is told
		// via the deletions set instead.
		dc.deletedMIDs = append(dc.deletedMIDs, mid)
		return nil
	}
	if err != nil {
		return err
	}
	msg, err := dc.store.OpenMessage(int64(mid))
	if err != nil {
		return err
	}
	isFAI := assoc.Valid && assoc.Int64 != 0

	ck, err := changeKey(dc.replica, uint64(cn))
	if err != nil {
		return err
	}
	pcl, err := predecessorChangeList(dc.replica, uint64(cn))
	if err != nil {
		return err
	}
	header := mapi.PropertyValues{
		{Tag: mapi.PrSourceKey, Value: sourceKey(dc.replica, mid)},
		{Tag: mapi.PrLastModificationTime, Value: lastModTime(msg.Props)},
		{Tag: mapi.PrChangeKey, Value: ck},
		{Tag: mapi.PrPredecessorChangeList, Value: pcl},
		{Tag: mapi.PrAssociated, Value: isFAI},
	}
	if dc.extraFlags&SyncExtraFlagEID != 0 {
		header = append(header, mapi.TaggedPropVal{Tag: mapi.PrMid, Value: int64(mapi.MakeEIDEx(homeReplID, mid))})
	}
	if dc.extraFlags&SyncExtraFlagMessageSize != 0 {
		header = append(header, mapi.TaggedPropVal{Tag: mapi.PrMessageSize, Value: int32(size)})
	}
	if dc.extraFlags&SyncExtraFlagCN != 0 {
		header = append(header, mapi.TaggedPropVal{Tag: mapi.PrChangeNumber, Value: int64(mapi.MakeEIDEx(homeReplID, uint64(cn)))})
	}

	dc.producer.WriteMarker(ics.MarkerIncrSyncChg)
	if err := dc.writeProps(header); err != nil {
		return err
	}
	dc.producer.WriteMarker(ics.MarkerIncrSyncMessage)
	if err := dc.writeProps(dc.filter(msg.Props)); err != nil {
		return err
	}

	if err := dc.writeProp(ics.StreamProp{Tag: mapi.PropTag(ics.MetaTagFXDelProp), Value: int32(mapi.PrMessageRecipients)}); err != nil {
		return err
	}
	for _, r := range msg.Recipients {
		dc.producer.WriteMarker(ics.MarkerStartRecip)
		if err := dc.writeProps(dc.filter(r)); err != nil {
			return err
		}
		dc.producer.WriteMarker(ics.MarkerEndToRecip)
	}
	if err := dc.writeProp(ics.StreamProp{Tag: mapi.PropTag(ics.MetaTagFXDelProp), Value: int32(mapi.PrMessageAttachments)}); err != nil {
		return err
	}
	for _, a := range msg.Attachments {
		dc.producer.WriteMarker(ics.MarkerNewAttach)
		if err := dc.writeProps(dc.filter(a.Props)); err != nil {
			return err
		}
		dc.producer.WriteMarker(ics.MarkerEndAttach)
	}
	return nil
}

// writeDeletions emits INCRSYNCDEL with the deleted and (unless soft deletions
// are suppressed) no-longer-in-scope id sets, both replica-id-keyed. Emits
// nothing when there is nothing to delete.
func (dc *DownloadContext) writeDeletions() error {
	var props mapi.PropertyValues
	if len(dc.deletedMIDs) > 0 {
		b, err := looseIDSetBytes(dc.deletedMIDs)
		if err != nil {
			return err
		}
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PropTag(ics.MetaTagIdsetDeleted), Value: b})
	}
	if dc.syncFlags&SyncNoSoftDeletions == 0 && len(dc.nolongerMIDs) > 0 {
		b, err := looseIDSetBytes(dc.nolongerMIDs)
		if err != nil {
			return err
		}
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PropTag(ics.MetaTagIdsetNoLongerInScope), Value: b})
	}
	if len(props) == 0 {
		return nil
	}
	dc.producer.WriteMarker(ics.MarkerIncrSyncDel)
	return dc.writeProps(props)
}

// writeReadState emits INCRSYNCREAD with the read and unread id sets. Emits
// nothing when no read state changed.
func (dc *DownloadContext) writeReadState() error {
	var props mapi.PropertyValues
	if len(dc.readMIDs) > 0 {
		b, err := looseIDSetBytes(dc.readMIDs)
		if err != nil {
			return err
		}
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PropTag(ics.MetaTagIdsetRead), Value: b})
	}
	if len(dc.unreadMIDs) > 0 {
		b, err := looseIDSetBytes(dc.unreadMIDs)
		if err != nil {
			return err
		}
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PropTag(ics.MetaTagIdsetUnread), Value: b})
	}
	if len(props) == 0 {
		return nil
	}
	dc.producer.WriteMarker(ics.MarkerIncrSyncRead)
	return dc.writeProps(props)
}

// writeState emits INCRSYNCSTATEBEGIN + the new high-water state + INCRSYNCSTATEEND.
// The state the client adopts is rebuilt fresh: given = the keep set, and the
// seen/seenFAI/read change-number sets become a single high-water range over the
// enabled classes ([MS-OXCFXICS] 3.3.5.13; the download replaces, never merges).
func (dc *DownloadContext) writeState() error {
	var out *ics.State
	if dc.syncType == SyncTypeHierarchy {
		out = ics.NewState(ics.HierarchyDown, dc.mapper)
	} else {
		out = ics.NewState(ics.ContentsDown, dc.mapper)
	}
	for _, id := range dc.givenKeep {
		out.Given().Append(mapi.MakeEIDEx(homeReplID, id))
	}
	if dc.syncType == SyncTypeHierarchy {
		if dc.lastCN != 0 {
			out.Seen().AppendRange(homeReplID, 1, dc.lastCN)
		}
	} else {
		if dc.syncFlags&SyncNormal != 0 && dc.lastCN != 0 {
			out.Seen().AppendRange(homeReplID, 1, dc.lastCN)
		}
		if dc.syncFlags&SyncAssociated != 0 && dc.lastCN != 0 {
			out.SeenFAI().AppendRange(homeReplID, 1, dc.lastCN)
		}
		if dc.syncFlags&SyncReadState != 0 && dc.lastReadCN != 0 {
			out.Read().AppendRange(homeReplID, 1, dc.lastReadCN)
		}
	}
	props, err := out.Serialize()
	if err != nil {
		return err
	}
	dc.producer.WriteMarker(ics.MarkerIncrSyncStateBegin)
	if err := dc.writePropsFX(props); err != nil {
		return err
	}
	dc.producer.WriteMarker(ics.MarkerIncrSyncStateEnd)
	return nil
}

// filter applies the SyncConfigure property filter: an inclusion list under
// SYNC_ONLY_SPECIFIED_PROPS (keep only listed tags), otherwise an exclusion list
// (drop listed tags). An empty list keeps everything.
func (dc *DownloadContext) filter(props mapi.PropertyValues) mapi.PropertyValues {
	if len(dc.proptags) == 0 {
		return props
	}
	include := dc.syncFlags&SyncOnlySpecifiedProps != 0
	out := make(mapi.PropertyValues, 0, len(props))
	for _, p := range props {
		_, listed := dc.proptags[p.Tag]
		if listed == include {
			out = append(out, p)
		}
	}
	return out
}

// writeProps converts a stored property bag to stream properties (resolving
// named-property ids to their GUID/kind/name) and writes them through the
// producer.
func (dc *DownloadContext) writeProps(props mapi.PropertyValues) error {
	for _, p := range props {
		sp, err := dc.toStreamProp(p)
		if err != nil {
			return err
		}
		if err := dc.writeProp(sp); err != nil {
			return err
		}
	}
	return nil
}

// writePropsFX writes already-built stream properties (the serialized state
// meta-tags) through the producer.
func (dc *DownloadContext) writePropsFX(props []ics.StreamProp) error {
	for _, p := range props {
		if err := dc.writeProp(p); err != nil {
			return err
		}
	}
	return nil
}

func (dc *DownloadContext) writeProp(sp ics.StreamProp) error {
	if err := dc.producer.WriteProp(sp); err != nil {
		return fmt.Errorf("objectstore: write %s to sync stream: %w", sp.Tag, err)
	}
	return nil
}

// toStreamProp maps a stored property to a stream property, resolving a
// named-property id (>= 0x8000) to the GUID/kind/name the stream carries inline
// so the receiver can remap it. A named id with no mapping is an error rather
// than a silent drop.
func (dc *DownloadContext) toStreamProp(p mapi.TaggedPropVal) (ics.StreamProp, error) {
	sp := ics.StreamProp{Tag: p.Tag, Value: p.Value}
	if propid := uint16(uint32(p.Tag) >> 16); uint64(propid) >= namedPropBase {
		name, ok, err := dc.store.NamedPropName(propid)
		if err != nil {
			return sp, err
		}
		if !ok {
			return sp, fmt.Errorf("objectstore: unresolved named property id %#x", propid)
		}
		sp.Name = &name
	}
	return sp, nil
}

// looseIDSetBytes serializes a replica-id-keyed (home) id set of bare GC values,
// the form the deletion and read-state meta-tags carry ([MS-OXCFXICS] 3.3.5.13).
func looseIDSetBytes(vals []uint64) ([]byte, error) {
	set := ics.NewIDSet(ics.FormIDLoose, nil)
	for _, v := range vals {
		set.AppendRange(homeReplID, v, v)
	}
	return set.Serialize()
}

// lastModTime returns the message's last-modification NT time, or now when it
// carries none (the change header requires the property).
func lastModTime(props mapi.PropertyValues) uint64 {
	if v, ok := props.Get(mapi.PrLastModificationTime); ok {
		if t, ok := v.(uint64); ok {
			return t
		}
	}
	return mapi.UnixToNTTime(time.Now())
}
