import { afterEach, beforeEach, describe, expect, it } from "vitest"
import { setDisplayTimeZone } from "@/utils/date"
import {
  dayKeyOf,
  dayLabel,
  eventHeightMinutes,
  eventsOnDay,
  eventTopMinutes,
  gridDay,
  monthMatrix,
  moveCursor,
  todayGridDay,
  wallInput,
  wallTime,
  weekDays,
} from "./calendarGrid"

// The grid lays out the display zone's days whatever zone the browser is in.
// New York is UTC-5 in November and UTC-4 in September.
beforeEach(() => setDisplayTimeZone("America/New_York"))
afterEach(() => setDisplayTimeZone(""))

describe("grid days", () => {
  it("starts on the day it is in the display zone", () => {
    // 02:00 UTC on 27 November is still 26 November in New York.
    expect(dayKeyOf(todayGridDay(new Date("2026-11-27T02:00:00Z")))).toBe("2026-11-26")
    expect(dayLabel(gridDay("2026-11-26"), { day: "numeric" })).toBe("26")
  })

  it("fills the month and the week from the chosen first weekday", () => {
    const month = monthMatrix(gridDay("2026-11-15"), 1)
    expect(month).toHaveLength(42)
    expect(dayKeyOf(month[0])).toBe("2026-10-26")
    expect(weekDays(gridDay("2026-11-26"), 7, 0).map(dayKeyOf)).toEqual([
      "2026-11-22", "2026-11-23", "2026-11-24", "2026-11-25", "2026-11-26", "2026-11-27", "2026-11-28",
    ])
  })

  it("moves by a day, a week, or to the first of a month", () => {
    const day = gridDay("2026-12-31")
    expect(dayKeyOf(moveCursor(day, "day", 1))).toBe("2027-01-01")
    expect(dayKeyOf(moveCursor(day, "week", -1))).toBe("2026-12-24")
    expect(dayKeyOf(moveCursor(day, "month", 1))).toBe("2027-01-01")
  })
})

describe("event boxes", () => {
  const day = gridDay("2026-11-26")

  it("places a timed event at its wall-clock minute in the display zone", () => {
    const ev = { start: "2026-11-26T14:00:00Z", end: "2026-11-26T15:30:00Z" }
    expect(eventTopMinutes(ev, day)).toBe(9 * 60)
    expect(eventHeightMinutes(ev, day)).toBe(90)
  })

  it("puts an event on the display zone's day, not the UTC day", () => {
    const late = { start: "2026-11-27T02:00:00Z" }
    const allDay = { start: "2026-11-27", allDay: true }
    expect(eventsOnDay([late, allDay], day, false)).toEqual([late])
    expect(eventsOnDay([late, allDay], gridDay("2026-11-27"), true)).toEqual([allDay])
  })

  it("clamps an event that runs past midnight to the day", () => {
    const ev = { start: "2026-11-27T04:00:00Z", end: "2026-11-27T07:00:00Z" }
    expect(eventHeightMinutes(ev, day)).toBe(60)
  })
})

describe("wall clocks", () => {
  it("turns a grid minute into the display zone's input and instant", () => {
    const day = gridDay("2026-09-25")
    expect(wallInput(day, 9 * 60 + 30)).toBe("2026-09-25T09:30")
    expect(wallInput(day, 24 * 60)).toBe("2026-09-26T00:00")
    expect(wallTime(day, 9 * 60).toISOString()).toBe("2026-09-25T13:00:00.000Z")
  })
})
