// Package oxcical converts iCalendar (RFC 5545) calendar objects to and from the
// MAPI appointment model, mirroring how internal/oxvcard converts vCard contacts
// and internal/oxcmail converts internet mail. It is the store-layer converter:
// the DAV protocol layer (internal/dav) moves opaque .ics blobs in and out and
// never parses iCalendar itself.
//
// A VEVENT becomes an IPM.Appointment message whose fields are properties on the
// message object (no bespoke calendar table). The conversion is hybrid:
//
//   - A non-recurring timed or all-day event is synthesized into MAPI properties
//     (start/end whole, location, busy status, subtype, etc.); Export rebuilds the
//     .ics from those properties — the same round-trip path mail takes.
//   - A recurring event (one carrying RRULE or RECURRENCE-ID) is preserved
//     VERBATIM in PrIcalOriginal and served back unchanged, because v1 does not
//     synthesize the binary recurrence pattern. This mirrors the S/MIME verbatim
//     strategy in internal/objectstore.
//
// Timed events resolve to a UTC instant on import (TZID via time.LoadLocation) and
// export as DTSTART:...Z; the standalone timezone blob is deferred. All-day events
// are floating VALUE=DATE.
package oxcical
