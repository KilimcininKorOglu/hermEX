package ews

import (
	"encoding/xml"
	"net/http"
	"slices"
	"strconv"
)

// dstRule is a zone's daylight-saving rule: the daylight total offset and the
// recurring spring-forward and fall-back transitions. Every served zone changes
// on a Sunday, so the day of week is implicit. start* moves the clock into
// Daylight; end* moves it back to Standard. The week is the occurrence of the
// Sunday in its month (1-4, or 5 for the last). The time is the local wall-clock
// of the transition, as an xs:duration.
type dstRule struct {
	dstBias    int // daylight total bias in minutes (UTC = local + bias)
	startMonth int
	startWeek  int
	startTime  string
	endMonth   int
	endWeek    int
	endTime    string
}

// tzRule is a served time zone: its Windows id, display name, standard total bias
// (minutes, UTC = local + bias, so west of UTC is positive), and optional DST
// rule. The set is curated, not exhaustive — the reference serves no time zones at
// all, so this covers the common Windows ids a client is likely to request.
type tzRule struct {
	id      string
	name    string
	stdBias int
	dst     *dstRule
}

// serverTimeZones is the curated zone set. US zones share one DST rule (second
// Sunday of March to first Sunday of November, 02:00 local); EU zones share
// another (last Sunday of March to last Sunday of October, anchored at 01:00 UTC).
var serverTimeZones = []tzRule{
	{id: "UTC", name: "(UTC) Coordinated Universal Time", stdBias: 0},
	{id: "GMT Standard Time", name: "(UTC+00:00) Dublin, Edinburgh, Lisbon, London", stdBias: 0,
		dst: &dstRule{dstBias: -60, startMonth: 3, startWeek: 5, startTime: "PT1H", endMonth: 10, endWeek: 5, endTime: "PT2H"}},
	{id: "W. Europe Standard Time", name: "(UTC+01:00) Amsterdam, Berlin, Bern, Rome, Stockholm, Vienna", stdBias: -60,
		dst: &dstRule{dstBias: -120, startMonth: 3, startWeek: 5, startTime: "PT2H", endMonth: 10, endWeek: 5, endTime: "PT3H"}},
	{id: "Turkey Standard Time", name: "(UTC+03:00) Istanbul", stdBias: -180},
	{id: "Eastern Standard Time", name: "(UTC-05:00) Eastern Time (US & Canada)", stdBias: 300,
		dst: &dstRule{dstBias: 240, startMonth: 3, startWeek: 2, startTime: "PT2H", endMonth: 11, endWeek: 1, endTime: "PT2H"}},
	{id: "Pacific Standard Time", name: "(UTC-08:00) Pacific Time (US & Canada)", stdBias: 480,
		dst: &dstRule{dstBias: 420, startMonth: 3, startWeek: 2, startTime: "PT2H", endMonth: 11, endWeek: 1, endTime: "PT2H"}},
}

// --- request / response wire types ---

type getServerTimeZonesRequest struct {
	Ids struct {
		IDs []string `xml:"Id"`
	} `xml:"Ids"`
}

type getServerTimeZonesResponse struct {
	XMLName  xml.Name                            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetServerTimeZonesResponse"`
	Messages []getServerTimeZonesResponseMessage `xml:"ResponseMessages>GetServerTimeZonesResponseMessage"`
}

type getServerTimeZonesResponseMessage struct {
	ResponseClass string        `xml:"ResponseClass,attr"`
	ResponseCode  string        `xml:"ResponseCode"`
	Definitions   tzDefinitions `xml:"TimeZoneDefinitions"`
}

// tzDefinitions switches the subtree to the types namespace: TimeZoneDefinitions
// is a messages-namespace element, but each TimeZoneDefinition (and everything
// under it) is types-namespace.
type tzDefinitions struct {
	Defs []tzDefinition `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeZoneDefinition"`
}

type tzDefinition struct {
	ID                string        `xml:"Id,attr"`
	Name              string        `xml:"Name,attr"`
	Periods           []tzPeriod    `xml:"Periods>Period"`
	TransitionsGroups []tzGroup     `xml:"TransitionsGroups>TransitionsGroup,omitempty"`
	Transitions       tzTransitions `xml:"Transitions"`
}

type tzPeriod struct {
	Bias string `xml:"Bias,attr"`
	Name string `xml:"Name,attr"`
	ID   string `xml:"Id,attr"`
}

type tzGroup struct {
	ID          string        `xml:"Id,attr"`
	Transitions []tzRecurring `xml:"RecurringDayTransition"`
}

type tzRecurring struct {
	To         tzTarget `xml:"To"`
	TimeOffset string   `xml:"TimeOffset"`
	Month      int      `xml:"Month"`
	DayOfWeek  string   `xml:"DayOfWeek"`
	Occurrence int      `xml:"Occurrence"`
}

type tzTransitions struct {
	Items []tzTransition `xml:"Transition"`
}

type tzTransition struct {
	To tzTarget `xml:"To"`
}

// tzTarget is a Transition's <To>: the target period or group id (chardata) with
// a required Kind attribute.
type tzTarget struct {
	Kind  string `xml:"Kind,attr"`
	Value string `xml:",chardata"`
}

// handleGetServerTimeZones answers GetServerTimeZones: it returns the curated zone
// definitions, filtered to the requested ids when an Ids list is given (an unknown
// id is simply absent from the result). ReturnFullTimeZoneData is ignored — full
// definitions are always returned, which never breaks a client that asked for
// less.
func (s *Server) handleGetServerTimeZones(w http.ResponseWriter, inner []byte, _ *session) {
	var req getServerTimeZonesRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetServerTimeZones: "+err.Error())
		return
	}
	want := req.Ids.IDs
	var defs []tzDefinition
	for _, z := range serverTimeZones {
		if len(want) == 0 || slices.Contains(want, z.id) {
			defs = append(defs, buildTimeZoneDefinition(z))
		}
	}
	writeResponse(w, getServerTimeZonesResponse{Messages: []getServerTimeZonesResponseMessage{{
		ResponseClass: "Success", ResponseCode: "NoError",
		Definitions: tzDefinitions{Defs: defs},
	}}})
}

// buildTimeZoneDefinition expands a zone rule into its wire definition: one Period
// for a fixed-offset zone (with a top-level Transition pointing at it), or two
// Periods plus a TransitionsGroup of recurring transitions for a DST zone (with a
// top-level Transition pointing at the group).
func buildTimeZoneDefinition(z tzRule) tzDefinition {
	stdID := "trule:" + z.id + "/Standard"
	def := tzDefinition{
		ID:      z.id,
		Name:    z.name,
		Periods: []tzPeriod{{Bias: minutesToDuration(z.stdBias), Name: "Standard", ID: stdID}},
	}
	if z.dst == nil {
		def.Transitions.Items = []tzTransition{{To: tzTarget{Kind: "Period", Value: stdID}}}
		return def
	}
	dltID := "trule:" + z.id + "/Daylight"
	def.Periods = append(def.Periods, tzPeriod{Bias: minutesToDuration(z.dst.dstBias), Name: "Daylight", ID: dltID})
	def.TransitionsGroups = []tzGroup{{ID: "0", Transitions: []tzRecurring{
		{To: tzTarget{Kind: "Period", Value: dltID}, TimeOffset: z.dst.startTime, Month: z.dst.startMonth, DayOfWeek: "Sunday", Occurrence: z.dst.startWeek},
		{To: tzTarget{Kind: "Period", Value: stdID}, TimeOffset: z.dst.endTime, Month: z.dst.endMonth, DayOfWeek: "Sunday", Occurrence: z.dst.endWeek},
	}}}
	def.Transitions.Items = []tzTransition{{To: tzTarget{Kind: "Group", Value: "0"}}}
	return def
}

// minutesToDuration renders a bias in minutes as an xs:duration: 0 → "PT0S",
// 480 → "PT8H", -60 → "-PT1H", -330 → "-PT5H30M".
func minutesToDuration(min int) string {
	if min == 0 {
		return "PT0S"
	}
	sign := ""
	if min < 0 {
		sign = "-"
		min = -min
	}
	out := sign + "PT"
	if h := min / 60; h > 0 {
		out += strconv.Itoa(h) + "H"
	}
	if m := min % 60; m > 0 {
		out += strconv.Itoa(m) + "M"
	}
	return out
}
