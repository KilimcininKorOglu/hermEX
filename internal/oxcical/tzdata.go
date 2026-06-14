package oxcical

// Embed the IANA time-zone database in any binary that links oxcical. The
// distroless runtime images carry no system zoneinfo, so without this
// time.LoadLocation would fail at runtime and every TZID-bearing event would
// silently mis-convert to the wrong instant — a "green in dev, broken in the
// container" bug. The converter that depends on the zone data carries it.
import _ "time/tzdata"
