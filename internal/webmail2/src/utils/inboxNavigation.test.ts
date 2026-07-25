import { describe, it, expect, beforeEach } from "vitest"
import {
  clampPageSize,
  getInboxNavMode,
  getInboxPageSize,
  setInboxNavigation,
  DEFAULT_PAGE_SIZE,
  MIN_PAGE_SIZE,
  MAX_PAGE_SIZE,
} from "@/utils/inboxNavigation"

describe("inboxNavigation", () => {
  beforeEach(() => {
    // Clear the two preference cookies between cases.
    document.cookie = "hermex-inbox-nav-mode=; Path=/; Max-Age=0"
    document.cookie = "hermex-inbox-page-size=; Path=/; Max-Age=0"
  })

  it("defaults to pagination at the default page size when unset", () => {
    expect(getInboxNavMode()).toBe("pagination")
    expect(getInboxPageSize()).toBe(DEFAULT_PAGE_SIZE)
  })

  it("clamps page size to the [MIN, MAX] range and rounds", () => {
    expect(clampPageSize(5)).toBe(MIN_PAGE_SIZE)
    expect(clampPageSize(9999)).toBe(MAX_PAGE_SIZE)
    expect(clampPageSize(50.6)).toBe(51)
    expect(clampPageSize(Number.NaN)).toBe(DEFAULT_PAGE_SIZE)
  })

  it("round-trips the chosen mode and page size through cookies", () => {
    setInboxNavigation("infinite", 100)
    expect(getInboxNavMode()).toBe("infinite")
    expect(getInboxPageSize()).toBe(100)
  })

  it("clamps an out-of-range page size on write", () => {
    setInboxNavigation("pagination", 1000)
    expect(getInboxPageSize()).toBe(MAX_PAGE_SIZE)
  })

  it("treats an unknown stored mode as pagination", () => {
    document.cookie = "hermex-inbox-nav-mode=weird; Path=/"
    expect(getInboxNavMode()).toBe("pagination")
  })
})
