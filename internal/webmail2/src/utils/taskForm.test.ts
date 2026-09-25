import { describe, expect, it } from "vitest"
import { dateInputValue, emptyTaskForm, taskFormOf, taskInputOf } from "./taskForm"

describe("taskFormOf", () => {
  it("fills absent fields with the form defaults", () => {
    expect(taskFormOf({ uid: "u1", summary: "Plan", completed: false })).toEqual({ ...emptyTaskForm, summary: "Plan" })
  })

  it("keeps every stored field", () => {
    const task = {
      uid: "u1", summary: "Plan", start: "2026-09-01", due: "2026-09-30T12:00:00Z", status: 1, percent: 40,
      priority: 2, reminder: true, categories: ["Red"], recurrence: "FREQ=DAILY;INTERVAL=1", owner: "bob@hermex.test",
      description: "notes", completed: false,
    }
    const { uid: _uid, completed: _completed, ...fields } = task
    expect(taskFormOf(task)).toEqual(fields)
  })
})

describe("taskInputOf", () => {
  it("sends empty text fields and no categories as absent, and keeps completed", () => {
    expect(taskInputOf({ ...emptyTaskForm, summary: "  Plan  " }, true)).toEqual({
      summary: "Plan", start: undefined, due: undefined, status: 0, percent: 0, priority: 1, reminder: false,
      categories: undefined, recurrence: undefined, description: undefined, owner: undefined, completed: true,
    })
  })

  it("round-trips a filled form", () => {
    const form = { ...emptyTaskForm, summary: "Plan", due: "2026-09-30", categories: ["Red"], owner: "bob@hermex.test" }
    expect(taskInputOf(form, false)).toMatchObject({ due: "2026-09-30", categories: ["Red"], owner: "bob@hermex.test", completed: false })
  })
})

describe("dateInputValue", () => {
  it("trims a timestamp to its date and keeps a short value", () => {
    expect(dateInputValue("2026-09-30T12:00:00Z")).toBe("2026-09-30")
    expect(dateInputValue("2026-09")).toBe("2026-09")
  })
})
