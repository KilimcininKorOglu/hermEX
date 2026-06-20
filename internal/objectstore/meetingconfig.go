package objectstore

import "hermex/internal/mapi"

// MeetingConfig is a mailbox's automatic meeting-request processing configuration
// (PR_SCHDINFO_*). AutoAccept is the master switch: when false the mailbox does no
// automatic processing and meeting requests are delivered for manual handling. When
// true, conflict-free requests are accepted; DeclineRecurring and DeclineConflict
// additionally decline recurring or conflicting requests.
type MeetingConfig struct {
	AutoAccept       bool
	DeclineRecurring bool
	DeclineConflict  bool
}

// GetMeetingConfig reads the mailbox's automatic meeting-processing settings; an
// unset property reads as false (the default: no automatic processing).
func (s *Store) GetMeetingConfig() (MeetingConfig, error) {
	props, err := s.GetStoreProperties(
		mapi.PrScheduleInfoAutoAccept,
		mapi.PrScheduleInfoDisallowRecurring,
		mapi.PrScheduleInfoDisallowOverlap,
	)
	if err != nil {
		return MeetingConfig{}, err
	}
	return MeetingConfig{
		AutoAccept:       boolProp(props, mapi.PrScheduleInfoAutoAccept),
		DeclineRecurring: boolProp(props, mapi.PrScheduleInfoDisallowRecurring),
		DeclineConflict:  boolProp(props, mapi.PrScheduleInfoDisallowOverlap),
	}, nil
}

// SetMeetingConfig replaces the mailbox's automatic meeting-processing settings.
func (s *Store) SetMeetingConfig(cfg MeetingConfig) error {
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrScheduleInfoAutoAccept, Value: cfg.AutoAccept},
		{Tag: mapi.PrScheduleInfoDisallowRecurring, Value: cfg.DeclineRecurring},
		{Tag: mapi.PrScheduleInfoDisallowOverlap, Value: cfg.DeclineConflict},
	})
}
