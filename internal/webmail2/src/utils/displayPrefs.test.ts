import { describe, it, expect, afterEach } from "vitest"
import { applyUnreadBorder } from "./displayPrefs"

describe("applyUnreadBorder", () => {
  afterEach(() => {
    delete document.documentElement.dataset.unreadBorder
  })

  it("sets the data attribute when on", () => {
    applyUnreadBorder(true)
    expect(document.documentElement.dataset.unreadBorder).toBe("true")
  })

  it("removes the data attribute when off", () => {
    applyUnreadBorder(true)
    applyUnreadBorder(false)
    expect(document.documentElement.dataset.unreadBorder).toBeUndefined()
  })
})
