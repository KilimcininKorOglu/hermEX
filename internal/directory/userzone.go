package directory

import "time"

// userZoneReader is the optional capability that answers a user's stored IANA
// time zone (the users.timezone column). SQLDirectory satisfies it; a directory
// that does not is not an error, the caller just has no zone to use.
type userZoneReader interface {
	GetUser(string) (UserDetail, bool, error)
}

// UserZone returns the mailbox owner's time zone, or nil when the directory
// cannot answer, the user is unknown, or the stored name is empty or not an IANA
// zone. It is how an inbound path learns the wall clock a floating iCalendar time
// means: such a value carries no zone of its own and is defined as the reader's
// own local time, so read without one it lands wrong by the owner's offset.
//
// nil is a usable answer, not a failure: the caller reads the value as UTC and
// records that it did.
func UserZone(a any, address string) *time.Location {
	rd, ok := a.(userZoneReader)
	if !ok {
		return nil
	}
	u, found, err := rd.GetUser(address)
	if err != nil || !found || u.Timezone == "" {
		return nil
	}
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return nil
	}
	return loc
}
