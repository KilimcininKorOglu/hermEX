package admin

import (
	"context"
	"net/http"
	"time"

	// The runtime image carries no zoneinfo, so the zone names an operator picks
	// must resolve from the binary itself.
	_ "time/tzdata"

	"hermex/internal/directory"
)

// zoneCookie caches the operator's time zone for the htmx fragments, which do
// not read the users record. The record, which webmail shares, is where it is
// stored.
const zoneCookie = "admin_tz"

// zoneKey carries the time zone the prefs middleware read from the users record.
type zoneKey struct{}

// withZone records the caller's stored time zone name on the request.
func withZone(r *http.Request, name string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), zoneKey{}, name))
}

// requestZone is the zone a response renders times in: the caller's stored
// choice, then the zone cookie, then UTC. A name that is not a zone reads as
// UTC, since the cookie is whatever the browser sent.
func requestZone(r *http.Request) *time.Location {
	name, ok := r.Context().Value(zoneKey{}).(string)
	if !ok {
		name = requestCookie(r, zoneCookie)
	}
	if name == "" || !directory.ValidTimezone(name) {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// syncZoneCookie brings the zone cookie in line with the stored zone and returns
// the request carrying it.
func syncZoneCookie(w http.ResponseWriter, r *http.Request, zone string) *http.Request {
	if requestCookie(r, zoneCookie) != zone {
		setPrefsCookie(w, zoneCookie, zone)
	}
	return withZone(r, zone)
}
