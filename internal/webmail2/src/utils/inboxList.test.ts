import { afterEach, describe, expect, it } from "vitest"
import { emptyListKey, formatSize, listKeysActive, nextListIndex, rowTone } from "./inboxList"

describe("formatSize", () => {
  it.each([
    [0, "< 1 KB"],
    [1023, "< 1 KB"],
    [1024, "1 KB"],
    [1536, "2 KB"],
    [1024 * 1024, "1.0 MB"],
    [5.25 * 1024 * 1024, "5.3 MB"],
  ])("renders %d bytes as %s", (bytes, want) => {
    expect(formatSize(bytes)).toBe(want)
  })
})

describe("rowTone", () => {
  const base = { read: true, flagged: false, viewMode: "list" as const, selected: false, previewed: false }

  it("tints a flagged row, darker when selected or previewed", () => {
    expect(rowTone({ ...base, flagged: true })).toContain("bg-amber-100/70")
    expect(rowTone({ ...base, flagged: true, selected: true })).toContain("bg-amber-200/70")
    expect(rowTone({ ...base, flagged: true, previewed: true })).toContain("bg-amber-200/70")
    expect(rowTone({ ...base, flagged: true, read: false, selected: true })).not.toContain("bg-primary")
  })

  it("shades an unread row only in the list view", () => {
    expect(rowTone({ ...base, read: false })).toBe("hover:bg-accent/50 bg-accent/5")
    expect(rowTone({ ...base, read: false, viewMode: "compact" })).toBe("hover:bg-accent/50")
  })

  it("adds the selected and previewed shades", () => {
    expect(rowTone({ ...base, selected: true })).toBe("hover:bg-accent/50 bg-primary/5")
    expect(rowTone({ ...base, previewed: true })).toContain("bg-primary/10")
  })
})

describe("nextListIndex", () => {
  it("moves down and up within the list", () => {
    expect(nextListIndex("j", 1, 5)).toBe(2)
    expect(nextListIndex("ArrowDown", 4, 5)).toBe(4)
    expect(nextListIndex("k", 1, 5)).toBe(0)
    expect(nextListIndex("ArrowUp", 0, 5)).toBe(0)
  })

  it("picks the first row when nothing is selected", () => {
    expect(nextListIndex("j", -1, 5)).toBe(0)
    expect(nextListIndex("k", -1, 5)).toBe(0)
  })

  it("ignores other keys", () => {
    expect(nextListIndex("Enter", 1, 5)).toBeNull()
    expect(nextListIndex("J", 1, 5)).toBeNull()
  })
})

describe("listKeysActive", () => {
  afterEach(() => {
    document.body.innerHTML = ""
  })

  const keydown = (target: EventTarget, init: KeyboardEventInit = {}) => {
    let seen: KeyboardEvent | null = null
    target.addEventListener("keydown", (e) => { seen = e as KeyboardEvent })
    target.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, ...init }))
    return seen as unknown as KeyboardEvent
  }

  it("accepts a plain key on the page", () => {
    expect(listKeysActive(keydown(document.body))).toBe(true)
  })

  it("refuses a key typed into a field or held with a modifier", () => {
    const input = document.body.appendChild(document.createElement("input"))
    expect(listKeysActive(keydown(input))).toBe(false)
    const area = document.body.appendChild(document.createElement("textarea"))
    expect(listKeysActive(keydown(area))).toBe(false)
    expect(listKeysActive(keydown(document.body, { ctrlKey: true }))).toBe(false)
    expect(listKeysActive(keydown(document.body, { metaKey: true }))).toBe(false)
    expect(listKeysActive(keydown(document.body, { altKey: true }))).toBe(false)
  })
})

describe("emptyListKey", () => {
  it("names the starred, unread and plain empty states", () => {
    expect(emptyListKey("starred", "all")).toBe("inbox.noStarredMessages")
    expect(emptyListKey("inbox", "starred")).toBe("inbox.noStarredMessages")
    expect(emptyListKey("inbox", "unread")).toBe("inbox.noUnreadMessages")
    expect(emptyListKey("sent", "all")).toBe("inbox.inboxEmpty")
  })
})
