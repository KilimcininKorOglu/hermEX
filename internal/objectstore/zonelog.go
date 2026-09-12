package objectstore

import (
	"hermex/internal/logging"
	"hermex/internal/oxcical"
)

// logZoneLosses records an inbound calendar whose times could not be bound to a
// zone. Such a time is stored as if it were UTC, so the appointment can sit hours
// away from the hour the organizer picked; without this line the only evidence is
// the wrong hour itself, and the zone id (the one fact needed to extend the zone
// table) is lost with the stream. Only the zone ids and a count are recorded, no
// message content.
func (s *Store) logZoneLosses(z *oxcical.ZoneLosses) {
	if z.Times() == 0 {
		return
	}
	s.logger.Emit(logging.Event{
		Level:     logging.LevelWarn,
		Subsystem: logging.Store,
		Name:      "calendar.zone_unresolved",
		Fields: logging.Fields{
			"mailbox": s.dir,
			"zones":   z.ZoneIDs(),
			"times":   z.Times(),
		},
	})
}
