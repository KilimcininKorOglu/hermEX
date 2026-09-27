import { describe, expect, it } from "vitest"
import {
  appearanceFromServer,
  calendarFromServer,
  dateToRFC3339,
  DEFAULT_CALENDAR,
  defaultAppearance,
  detectBrowser,
  formatStorageBytes,
  rfc3339ToDate,
  usagePercent,
} from "./settingsRecords"

describe("appearanceFromServer", () => {
  it("keeps stored values and fills the missing ones with defaults", () => {
    const rec = appearanceFromServer({ dateFormat: "dmy", unreadBorder: true, inboxPageSize: 25 })
    expect(rec.dateFormat).toBe("dmy")
    expect(rec.unreadBorder).toBe(true)
    expect(rec.inboxPageSize).toBe(25)
    expect(rec.timeFormat).toBe(defaultAppearance().timeFormat)
    expect(rec.mailListColumns).toEqual(defaultAppearance().mailListColumns)
  })

  it("never carries the server's read-only preview cap", () => {
    expect(appearanceFromServer({ previewMaxBytes: 5 })).not.toHaveProperty("previewMaxBytes")
  })
})

describe("calendarFromServer", () => {
  it("keeps stored values, including a zero reminder, and fills the rest", () => {
    const rec = calendarFromServer({ defaultReminder: 0, workDays: [0, 6] })
    expect(rec.defaultReminder).toBe(0)
    expect(rec.workDays).toEqual([0, 6])
    expect(rec.workDayStart).toBe(DEFAULT_CALENDAR.workDayStart)
  })
})

describe("detectBrowser", () => {
  it("names the browser before the engines its user agent also lists", () => {
    const chrome = "Mozilla/5.0 AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36"
    expect(detectBrowser(`${chrome} Edg/131.0.0.0`)).toBe("Edge 131")
    expect(detectBrowser(`${chrome} OPR/115.0.0.0`)).toBe("Opera 115")
    expect(detectBrowser(chrome)).toBe("Chrome 131")
    expect(detectBrowser("Mozilla/5.0 Version/18.1 Safari/605.1.15")).toBe("Safari 18")
    expect(detectBrowser("Mozilla/5.0 Gecko/20100101 Firefox/133.0")).toBe("Firefox 133")
    expect(detectBrowser("curl/8.0")).toBe("Unknown")
  })
})

describe("format helpers", () => {
  it("converts between a date input and RFC 3339", () => {
    expect(rfc3339ToDate(dateToRFC3339("2026-09-25"))).toBe("2026-09-25")
    expect(rfc3339ToDate(undefined)).toBe("")
    expect(dateToRFC3339("")).toBeUndefined()
    // A start bound is the local midnight the day begins.
    expect(new Date(dateToRFC3339("2026-09-25") ?? "").getHours()).toBe(0)
  })

  it("keeps the chosen end day inside the range", () => {
    const end = dateToRFC3339("2026-09-25", true) ?? ""
    const last = new Date(new Date(end).getTime() - 1)
    expect(last.getDate()).toBe(25)
    expect(new Date(end).getDate()).toBe(26)
    expect(rfc3339ToDate(end, true)).toBe("2026-09-25")
    // A month end rolls over into the next month.
    expect(rfc3339ToDate(dateToRFC3339("2026-09-30", true), true)).toBe("2026-09-30")
  })

  it("renders storage sizes and caps the usage share", () => {
    expect(formatStorageBytes(512)).toBe("512 B")
    expect(formatStorageBytes(2048)).toBe("2 KB")
    expect(formatStorageBytes(3 * 1024 ** 2)).toBe("3.0 MB")
    expect(formatStorageBytes(2 * 1024 ** 3)).toBe("2.00 GB")
    expect(usagePercent(50, 200)).toBe(25)
    expect(usagePercent(300, 200)).toBe(100)
  })
})
