package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// stamp is one point in time as a page shows it: Rel is a catalogue message such
// as "12 minutes ago" (empty a day or more from now), Short is the date and time
// in the operator's zone and language, Full adds the zone name for the tooltip,
// and ISO is the machine-readable instant. The zero stamp renders nothing.
type stamp struct {
	ISO, Short, Full, Rel string
}

// clock formats times for one response: in the operator's zone, relative to one
// moment, so every row on a page is measured from the same now.
type clock struct {
	loc  *time.Location
	now  time.Time
	lang string
}

// requestClock is the clock a response renders times with.
func requestClock(r *http.Request) clock {
	return clock{loc: requestZone(r), now: time.Now(), lang: requestLang(r)}
}

// stampLayouts are the date and time forms each language writes; a language
// without one uses the English form.
var stampLayouts = map[string]string{
	"en": "01/02/2006 3:04 PM",
	"tr": "02.01.2006 15:04",
}

// relativeLimit is how far from now a time still reads as a distance ("10 h
// ago"); from a day on the date and time say more.
const relativeLimit = 24 * time.Hour

// at renders t; the zero time renders as the zero stamp.
func (c clock) at(t time.Time) stamp {
	if t.IsZero() {
		return stamp{}
	}
	layout, ok := stampLayouts[c.lang]
	if !ok {
		layout = stampLayouts["en"]
	}
	short := t.In(c.loc).Format(layout)
	return stamp{
		ISO:   t.UTC().Format(time.RFC3339),
		Short: short,
		Full:  short + " " + zoneLabel(c.loc),
		Rel:   relativeMsg(t.Sub(c.now)),
	}
}

// At renders t for a template that ranges over raw times: {{template "when"
// ($.Clock.At .Time)}}.
func (c clock) At(t time.Time) stamp {
	return c.at(t)
}

// unix renders a unix time in seconds; 0 is the zero stamp.
func (c clock) unix(sec int64) stamp {
	if sec == 0 {
		return stamp{}
	}
	return c.at(time.Unix(sec, 0))
}

// zoneLabel names loc for a tooltip: the IANA name, or UTC.
func zoneLabel(loc *time.Location) string {
	if loc == time.UTC {
		return "UTC"
	}
	return loc.String()
}

// relativeMsg renders the distance d from now as a catalogue message: "3 hours
// ago" for a past time and "in 3 hours" for a future one. It returns "" from
// relativeLimit on, where the date says more than the distance.
func relativeMsg(d time.Duration) string {
	past := d <= 0
	if past {
		d = -d
	}
	if d >= relativeLimit {
		return ""
	}
	u, n := relativeUnit(d)
	if past {
		return msg(u.ago, itoa(n))
	}
	return msg(u.ahead, itoa(n))
}

// relUnit is one step of a relative time: its size and its past and future
// messages. A zero size is the step under a second, which names no number.
type relUnit struct {
	size       time.Duration
	ago, ahead string
}

var relUnits = []relUnit{
	{time.Hour, "time.hoursAgo", "time.inHours"},
	{time.Minute, "time.minutesAgo", "time.inMinutes"},
	{time.Second, "time.secondsAgo", "time.inSeconds"},
	{0, "time.justNow", "time.shortly"},
}

// relativeUnit picks the largest whole unit of d and the count of it.
func relativeUnit(d time.Duration) (relUnit, int64) {
	for _, u := range relUnits[:len(relUnits)-1] {
		if d >= u.size {
			return u, int64(d / u.size)
		}
	}
	return relUnits[len(relUnits)-1], 0
}

// durationMsg renders a length of time in seconds in its two largest units (days
// and hours, hours and minutes, minutes and seconds), because a raw second count
// stops being readable after the first few minutes.
func durationMsg(secs int64) string {
	if secs < 0 {
		secs = 0
	}
	d, h, m := secs/86400, secs%86400/3600, secs%3600/60
	switch {
	case d > 0:
		return msg("dur.daysHours", itoa(d), itoa(h))
	case h > 0:
		return msg("dur.hoursMinutes", itoa(h), itoa(m))
	case m > 0:
		return msg("dur.minutesSeconds", itoa(m), itoa(secs%60))
	}
	return msg("dur.seconds", itoa(secs))
}

// sizeUnits are the binary size steps a size is shown in.
var sizeUnits = []string{"size.bytes", "size.kb", "size.mb", "size.gb", "size.tb"}

// sizeMsg renders a byte count in the largest unit that keeps it at or above 1,
// with one decimal under 10 in lang's decimal separator ("1,5 MB" in Turkish).
func sizeMsg(bytes int64, lang string) string {
	v, i := float64(max(bytes, 0)), 0
	for v >= 1024 && i < len(sizeUnits)-1 {
		v /= 1024
		i++
	}
	if i == 0 || v >= 10 {
		return msg(sizeUnits[i], strconv.FormatFloat(v, 'f', 0, 64))
	}
	n := strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
	if lang == "tr" {
		n = strings.Replace(n, ".", ",", 1)
	}
	return msg(sizeUnits[i], n)
}

// itoa formats a count for a message argument.
func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
