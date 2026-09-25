import { describe, expect, it } from "vitest"
import type { CalendarEvent } from "@/utils/api"
import { eventTitleText } from "./event-title"

const base: CalendarEvent = { uid: "1", summary: "Review", start: "2026-09-10T09:00:00Z" }

describe("eventTitleText", () => {
  it("says a cancelled meeting is cancelled before its title", () => {
    expect(eventTitleText({ ...base, canceled: true }, "Canceled")).toBe("Canceled: Review")
  })

  it("leaves a meeting that takes place as its title", () => {
    expect(eventTitleText(base, "Canceled")).toBe("Review")
  })
})
