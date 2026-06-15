package ics

import (
	"fmt"

	"hermex/internal/mapi"
)

// StateType selects which idsets an ICS State holds and how AppendIDSet merges
// an uploaded set ([MS-OXCFXICS] 3.x). Download types rebuild state from the
// store's high-water marks; upload types accumulate across successive uploads.
type StateType uint8

const (
	ContentsDown StateType = iota
	ContentsUp
	HierarchyDown
	HierarchyUp
)

func (t StateType) isUp() bool { return t == ContentsUp || t == HierarchyUp }

// State is the ICS synchronization state: the four idsets a client and server
// exchange so the server can compute a delta. given = the ids the peer already
// has; seen / seenFAI / read = the change numbers it has seen for normal
// messages, FAI messages, and read state. Members are nil when not applicable to
// the type (per the allocation below).
type State struct {
	typ                        StateType
	given, seen, seenFAI, read *IDSet
	mapper                     ReplicaMapper
}

// NewState allocates the idsets a given type uses. pseen is always present;
// ContentsDown/ContentsUp use all four; HierarchyDown adds only given;
// HierarchyUp uses only seen. ([MS-OXCFXICS]; matched to the reference
// allocation.)
func NewState(typ StateType, mapper ReplicaMapper) *State {
	s := &State{typ: typ, mapper: mapper}
	mk := func() *IDSet { return NewIDSet(FormGUIDLoose, mapper) }
	s.seen = mk()
	switch typ {
	case ContentsDown, ContentsUp:
		s.given, s.seenFAI, s.read = mk(), mk(), mk()
	case HierarchyDown:
		s.given = mk()
	}
	return s
}

// Given, Seen, SeenFAI, and Read expose the idsets so the download path can seed
// them from the store's high-water marks and the upload path can query them. A
// nil result means the type does not track that set.
func (s *State) Given() *IDSet   { return s.given }
func (s *State) Seen() *IDSet    { return s.seen }
func (s *State) SeenFAI() *IDSet { return s.seenFAI }
func (s *State) Read() *IDSet    { return s.read }

// AppendIDSet folds an uploaded, serialized idset into the state by its meta-tag.
// The given set is replaced outright; the seen/seenFAI/read sets accumulate
// (union with the prior value) for upload types and are replaced for download
// types. The bytes are parsed as a packed GUID idset and converted to the
// queryable form via the state's replica mapper.
func (s *State) AppendIDSet(metaTag uint32, serialized []byte) error {
	set := NewIDSet(FormGUIDPacked, s.mapper)
	if err := set.Deserialize(serialized); err != nil {
		return fmt.Errorf("ics: deserialize state idset %#x: %w", metaTag, err)
	}
	if !set.Convert() {
		return fmt.Errorf("ics: cannot resolve replicas for state idset %#x", metaTag)
	}
	switch metaTag {
	case metaTagIdsetGiven, metaTagIdsetGiven1:
		s.given = set
	case metaTagCnsetSeen:
		s.seen = s.mergeUp(s.seen, set)
	case metaTagCnsetSeenFAI:
		if s.typ == ContentsUp {
			set = s.mergeUp(s.seenFAI, set)
		}
		s.seenFAI = set
	case metaTagCnsetRead:
		if s.typ == ContentsUp {
			set = s.mergeUp(s.read, set)
		}
		s.read = set
	default:
		return fmt.Errorf("ics: %#x is not a state meta-tag", metaTag)
	}
	return nil
}

// mergeUp unions the prior set into incoming for upload types (cumulative across
// uploads); for download types it returns incoming unchanged (replace). It never
// fails because both sets are loose after Convert.
func (s *State) mergeUp(prior, incoming *IDSet) *IDSet {
	if prior != nil && s.typ.isUp() && !prior.Empty() {
		incoming.Concatenate(prior)
	}
	return incoming
}

// Serialize emits the state as the meta-tag binary properties to write into a
// FastTransfer stream. The given set is emitted as the honest PT_BINARY
// MetaTagIdsetGiven1. Which sets are emitted is gated by type, matching the
// reference: download emits its full state; upload emits given/read only when
// non-empty.
func (s *State) Serialize() ([]StreamProp, error) {
	var out []StreamProp
	emit := func(metaTag uint32, set *IDSet) error {
		b, err := set.Serialize()
		if err != nil {
			return err
		}
		out = append(out, StreamProp{Tag: mapi.PropTag(metaTag), Value: b})
		return nil
	}

	if s.given != nil && (s.typ == ContentsDown || s.typ == HierarchyDown || (s.typ == ContentsUp && !s.given.Empty())) {
		if err := emit(metaTagIdsetGiven1, s.given); err != nil {
			return nil, err
		}
	}
	if err := emit(metaTagCnsetSeen, s.seen); err != nil {
		return nil, err
	}
	if s.seenFAI != nil && (s.typ == ContentsDown || s.typ == ContentsUp) {
		if err := emit(metaTagCnsetSeenFAI, s.seenFAI); err != nil {
			return nil, err
		}
	}
	if s.read != nil && (s.typ == ContentsDown || (s.typ == ContentsUp && !s.read.Empty())) {
		if err := emit(metaTagCnsetRead, s.read); err != nil {
			return nil, err
		}
	}
	return out, nil
}
