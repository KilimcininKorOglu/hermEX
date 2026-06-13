package objectstore

import (
	"time"

	"hermex/internal/mapi"
)

// Follow-up flag status values (PR_FLAG_STATUS, MS-OXOFLAG).
const (
	FlagStatusNone     int32 = 0
	FlagStatusComplete int32 = 1
	FlagStatusFlagged  int32 = 2
)

// Follow-up flag colors (PR_FOLLOWUP_ICON), the six Outlook colored flags.
const (
	FlagColorClear  int32 = 0
	FlagColorPurple int32 = 1
	FlagColorOrange int32 = 2
	FlagColorGreen  int32 = 3
	FlagColorYellow int32 = 4
	FlagColorBlue   int32 = 5
	FlagColorRed    int32 = 6
)

// FollowupFlag is a message's follow-up flag (MS-OXOFLAG): the status, its
// color, the optional follow-up text, and the due / complete times. The zero
// value is "no flag" (Status FlagStatusNone, no color); the times are zero when
// unset.
type FollowupFlag struct {
	Status   int32
	Color    int32
	Request  string
	DueBy    time.Time
	Complete time.Time
}

// namedProptag resolves a named property to its full store proptag of the given
// type. With create=false a name never written before returns ok=false; with
// create=true a fresh id is allocated. The id is stable across reopen.
func (s *Store) namedProptag(name mapi.PropertyName, typ mapi.PropType, create bool) (mapi.PropTag, bool, error) {
	ids, err := s.GetNamedPropIDs(create, []mapi.PropertyName{name})
	if err != nil {
		return 0, false, err
	}
	if ids[0] == 0 {
		return 0, false, nil
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(typ)), true, nil
}

// GetFollowupFlag reads a message's follow-up flag. A message with no flag
// returns the zero FollowupFlag.
func (s *Store) GetFollowupFlag(messageID int64) (FollowupFlag, error) {
	var f FollowupFlag
	tags := []mapi.PropTag{mapi.PrFlagStatus, mapi.PrFollowupIcon, mapi.PrFlagCompleteTime}
	reqTag, hasReq, err := s.namedProptag(mapi.NameFlagRequest, mapi.PtUnicode, false)
	if err != nil {
		return f, err
	}
	if hasReq {
		tags = append(tags, reqTag)
	}
	dueTag, hasDue, err := s.namedProptag(mapi.NameReminderSignalTime, mapi.PtSysTime, false)
	if err != nil {
		return f, err
	}
	if hasDue {
		tags = append(tags, dueTag)
	}
	props, err := s.GetMessageProperties(messageID, tags...)
	if err != nil {
		return f, err
	}
	if v, ok := props.Get(mapi.PrFlagStatus); ok {
		if n, ok := v.(int32); ok {
			f.Status = n
		}
	}
	if v, ok := props.Get(mapi.PrFollowupIcon); ok {
		if n, ok := v.(int32); ok {
			f.Color = n
		}
	}
	if v, ok := props.Get(mapi.PrFlagCompleteTime); ok {
		if nt, ok := v.(uint64); ok {
			f.Complete = mapi.NTTimeToUnix(nt)
		}
	}
	if hasReq {
		if v, ok := props.Get(reqTag); ok {
			if str, ok := v.(string); ok {
				f.Request = str
			}
		}
	}
	if hasDue {
		if v, ok := props.Get(dueTag); ok {
			if nt, ok := v.(uint64); ok {
				f.DueBy = mapi.NTTimeToUnix(nt)
			}
		}
	}
	return f, nil
}

// SetFollowupFlag writes a message's follow-up flag and keeps the IMAP \Flagged
// bit in sync: a flagged status sets \Flagged, complete or none clears it, so
// IMAP clients still see flagged mail. Status and color are the regular
// PR_FLAG_STATUS / PR_FOLLOWUP_ICON; the follow-up text and due time are named
// properties; the complete time is written (defaulting to now) when the status
// is complete.
func (s *Store) SetFollowupFlag(messageID int64, f FollowupFlag) error {
	var pv mapi.PropertyValues
	pv.Set(mapi.PrFlagStatus, f.Status)
	pv.Set(mapi.PrFollowupIcon, f.Color)
	if f.Status == FlagStatusComplete {
		t := f.Complete
		if t.IsZero() {
			t = time.Now()
		}
		pv.Set(mapi.PrFlagCompleteTime, mapi.UnixToNTTime(t))
	}
	if f.Request != "" {
		reqTag, _, err := s.namedProptag(mapi.NameFlagRequest, mapi.PtUnicode, true)
		if err != nil {
			return err
		}
		pv.Set(reqTag, f.Request)
	}
	if !f.DueBy.IsZero() {
		dueTag, _, err := s.namedProptag(mapi.NameReminderSignalTime, mapi.PtSysTime, true)
		if err != nil {
			return err
		}
		pv.Set(dueTag, mapi.UnixToNTTime(f.DueBy))
	}
	if err := s.SetMessageProperties(messageID, pv); err != nil {
		return err
	}
	return s.setIndexFlagged(messageID, f.Status == FlagStatusFlagged)
}

// ClearFollowupFlag removes a message's follow-up flag (status none, color
// clear) and clears the IMAP \Flagged bit.
func (s *Store) ClearFollowupFlag(messageID int64) error {
	return s.SetFollowupFlag(messageID, FollowupFlag{Status: FlagStatusNone, Color: FlagColorClear})
}

// setIndexFlagged sets or clears the IMAP \Flagged bit in the index for a
// message id, so the follow-up flag and the IMAP flag stay consistent.
func (s *Store) setIndexFlagged(messageID int64, flagged bool) error {
	v := 0
	if flagged {
		v = 1
	}
	_, err := s.idxdb.Exec(`UPDATE messages SET flagged=? WHERE message_id=?`, v, messageID)
	return err
}

// GetCategories reads a message's category list (PidNameKeywords). A message
// with no categories returns nil.
func (s *Store) GetCategories(messageID int64) ([]string, error) {
	tag, ok, err := s.namedProptag(mapi.NameKeywords, mapi.PtMvUnicode, false)
	if err != nil || !ok {
		return nil, err
	}
	props, err := s.GetMessageProperties(messageID, tag)
	if err != nil {
		return nil, err
	}
	if v, ok := props.Get(tag); ok {
		if cats, ok := v.([]string); ok {
			return cats, nil
		}
	}
	return nil, nil
}

// SetCategories writes a message's category list (PidNameKeywords) as a
// multivalue Unicode property. An empty list stores an empty multivalue.
func (s *Store) SetCategories(messageID int64, cats []string) error {
	tag, _, err := s.namedProptag(mapi.NameKeywords, mapi.PtMvUnicode, true)
	if err != nil {
		return err
	}
	var pv mapi.PropertyValues
	pv.Set(tag, cats)
	return s.SetMessageProperties(messageID, pv)
}
