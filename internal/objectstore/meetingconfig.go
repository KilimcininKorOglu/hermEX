package objectstore

import (
	"errors"

	"hermex/internal/mapi"
)

// hermexStoreNamespace is hermEX's private named-property GUID for store-level
// settings that have no MS-OXPROPS counterpart. A named property is used rather
// than a fixed proptag because the 0x8000 and up range those would have to live in
// is the named-property id space itself, so a hand-picked tag there would collide
// with whatever the store allocates next.
var hermexStoreNamespace = mapi.GUID{
	Data1: 0x4E2A7C61, Data2: 0x8B93, Data3: 0x4D05,
	Data4: [8]byte{0xA1, 0x77, 0x3C, 0x6E, 0x91, 0x0B, 0x25, 0x48},
}

// nameRemoveRequestOnResponse names the flag that decides whether responding to a
// meeting request also takes the request mail out of the Inbox.
var nameRemoveRequestOnResponse = mapi.PropertyName{
	Kind: mapi.MnidString, GUID: hermexStoreNamespace, Name: "RemoveMeetingRequestOnResponse",
}

// nameLeaveCancellationsUnprocessed names the flag that stops a delivered meeting
// cancellation from being applied to the calendar.
var nameLeaveCancellationsUnprocessed = mapi.PropertyName{
	Kind: mapi.MnidString, GUID: hermexStoreNamespace, Name: "LeaveMeetingCancellationsUnprocessed",
}

// MeetingConfig is a mailbox's meeting-request handling configuration. The first
// three fields are the automatic-processing settings (PR_SCHDINFO_*): AutoAccept is
// the master switch, and when it is false the mailbox does no automatic processing
// and meeting requests are delivered for manual handling; when it is true,
// conflict-free requests are accepted, and DeclineRecurring and DeclineConflict
// additionally decline recurring or conflicting requests.
//
// RemoveRequestOnResponse and LeaveCancellationsUnprocessed are independent of
// those three.
type MeetingConfig struct {
	AutoAccept       bool
	DeclineRecurring bool
	DeclineConflict  bool
	// RemoveRequestOnResponse moves the request mail to Deleted Items once the
	// response is recorded, the way Outlook's "delete meeting requests and
	// notifications from Inbox after responding" option does. It applies to every
	// response, whether a person made it or automatic processing did. It defaults
	// to false, so a mailbox keeps the request until someone turns this on.
	RemoveRequestOnResponse bool
	// LeaveCancellationsUnprocessed stops a delivered meeting cancellation from
	// marking the meeting cancelled in the calendar, so the attendee handles it by
	// hand. It defaults to false, so cancellations are processed the way Outlook
	// processes them; the field is phrased as the exception so that a
	// configuration that never names it keeps that default.
	LeaveCancellationsUnprocessed bool
}

// namedFlagTag resolves a private boolean flag's tag for this store. create
// allocates the named-property id, which a write needs and a read must not do.
func (s *Store) namedFlagTag(name mapi.PropertyName, create bool) (mapi.PropTag, error) {
	ids, err := s.GetNamedPropIDs(create, []mapi.PropertyName{name})
	if err != nil || len(ids) == 0 || ids[0] == 0 {
		return 0, err
	}
	return mapi.MakeTag(ids[0], mapi.PtBoolean), nil
}

// namedFlag reads a private boolean flag, false when the store never set it.
func (s *Store) namedFlag(name mapi.PropertyName) (bool, error) {
	tag, err := s.namedFlagTag(name, false)
	if err != nil || tag == 0 {
		return false, err
	}
	props, err := s.GetStoreProperties(tag)
	if err != nil {
		return false, err
	}
	return boolProp(props, tag), nil
}

// GetMeetingConfig reads the mailbox's meeting-request handling settings; an unset
// property reads as false (the default: no automatic processing of requests, the
// request mail is kept, and cancellations are processed).
func (s *Store) GetMeetingConfig() (MeetingConfig, error) {
	props, err := s.GetStoreProperties(
		mapi.PrScheduleInfoAutoAccept,
		mapi.PrScheduleInfoDisallowRecurring,
		mapi.PrScheduleInfoDisallowOverlap,
	)
	if err != nil {
		return MeetingConfig{}, err
	}
	remove, err1 := s.namedFlag(nameRemoveRequestOnResponse)
	leave, err2 := s.namedFlag(nameLeaveCancellationsUnprocessed)
	if err := errors.Join(err1, err2); err != nil {
		return MeetingConfig{}, err
	}
	return MeetingConfig{
		AutoAccept:                    boolProp(props, mapi.PrScheduleInfoAutoAccept),
		DeclineRecurring:              boolProp(props, mapi.PrScheduleInfoDisallowRecurring),
		DeclineConflict:               boolProp(props, mapi.PrScheduleInfoDisallowOverlap),
		RemoveRequestOnResponse:       remove,
		LeaveCancellationsUnprocessed: leave,
	}, nil
}

// SetMeetingConfig replaces the mailbox's meeting-request handling settings.
func (s *Store) SetMeetingConfig(cfg MeetingConfig) error {
	props := mapi.PropertyValues{
		{Tag: mapi.PrScheduleInfoAutoAccept, Value: cfg.AutoAccept},
		{Tag: mapi.PrScheduleInfoDisallowRecurring, Value: cfg.DeclineRecurring},
		{Tag: mapi.PrScheduleInfoDisallowOverlap, Value: cfg.DeclineConflict},
	}
	for name, value := range map[mapi.PropertyName]bool{
		nameRemoveRequestOnResponse:       cfg.RemoveRequestOnResponse,
		nameLeaveCancellationsUnprocessed: cfg.LeaveCancellationsUnprocessed,
	} {
		tag, err := s.namedFlagTag(name, true)
		if err != nil {
			return err
		}
		if tag != 0 {
			props = append(props, mapi.TaggedPropVal{Tag: tag, Value: value})
		}
	}
	return s.SetStoreProperties(props)
}
