import { describe, it, expect } from "vitest"
import { escapeICalText, icalDateTime, taskToVTodo, noteToText, safeItemName } from "./attachItem"
import { Task, Note } from "@/utils/api"

describe("escapeICalText", () => {
  it("escapes backslash, semicolon, comma, and newlines", () => {
    expect(escapeICalText("a;b,c\\d")).toBe("a\\;b\\,c\\\\d")
    expect(escapeICalText("line1\nline2")).toBe("line1\\nline2")
    expect(escapeICalText("win\r\nend")).toBe("win\\nend")
  })
})

describe("icalDateTime", () => {
  it("renders a bare date as a DATE value", () => {
    expect(icalDateTime("2026-02-10")).toEqual({ value: "20260210", isDate: true })
  })
  it("renders an RFC3339 timestamp as a UTC datetime", () => {
    expect(icalDateTime("2026-02-10T14:00:00Z")).toEqual({ value: "20260210T140000Z", isDate: false })
  })
  it("returns empty for an unparseable value", () => {
    expect(icalDateTime("not-a-date").value).toBe("")
  })
})

const baseTask: Task = { uid: "t-1", summary: "Ship release", completed: false }

describe("taskToVTodo", () => {
  it("wraps a VTODO in a VCALENDAR with UID and SUMMARY", () => {
    const ics = taskToVTodo(baseTask)
    expect(ics).toContain("BEGIN:VCALENDAR")
    expect(ics).toContain("BEGIN:VTODO")
    expect(ics).toContain("UID:t-1")
    expect(ics).toContain("SUMMARY:Ship release")
    expect(ics).toContain("END:VTODO")
    expect(ics.endsWith("END:VCALENDAR\r\n")).toBe(true)
  })
  it("maps completion and high priority", () => {
    expect(taskToVTodo({ ...baseTask, completed: true })).toContain("STATUS:COMPLETED")
    expect(taskToVTodo({ ...baseTask, priority: 2 })).toContain("PRIORITY:1")
    expect(taskToVTodo({ ...baseTask, priority: 0 })).toContain("PRIORITY:9")
  })
  it("emits a DATE-valued DUE for a bare date and escapes the summary", () => {
    const ics = taskToVTodo({ ...baseTask, summary: "a; b, c", due: "2026-03-01" })
    expect(ics).toContain("DUE;VALUE=DATE:20260301")
    expect(ics).toContain("SUMMARY:a\\; b\\, c")
  })
})

describe("noteToText", () => {
  it("joins title and body with a blank line", () => {
    const n: Note = { id: "n1", title: "Idea", body: "buy milk" }
    expect(noteToText(n)).toBe("Idea\r\n\r\nbuy milk")
  })
  it("falls back to just the body when untitled", () => {
    const n: Note = { id: "n2", title: "", body: "orphan" }
    expect(noteToText(n)).toBe("orphan")
  })
})

describe("safeItemName", () => {
  it("strips unsafe characters and trims separators", () => {
    expect(safeItemName("  My/Task:name  ", "task")).toBe("My_Task_name")
  })
  it("uses the fallback when nothing survives", () => {
    expect(safeItemName("///", "note")).toBe("note")
  })
})
