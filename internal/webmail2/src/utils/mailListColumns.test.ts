import { describe, it, expect, beforeEach } from "vitest"
import { defaultMailColumns, getMailColumns, setMailColumns } from "@/utils/mailListColumns"

// Clear the cookie between cases so each starts from a known state.
beforeEach(() => {
  document.cookie = "hermex-mail-columns=; Path=/; Max-Age=0"
})

describe("defaultMailColumns", () => {
  it("shows preview/attachment/importance/categories, hides size/flag", () => {
    expect(defaultMailColumns()).toEqual({
      preview: true,
      attachment: true,
      importance: true,
      categories: true,
      size: false,
      flag: false,
    })
  })
})

describe("getMailColumns", () => {
  it("returns the defaults when no cookie is set", () => {
    expect(getMailColumns()).toEqual(defaultMailColumns())
  })

  it("round-trips a stored set through setMailColumns", () => {
    const cols = { preview: false, attachment: true, importance: false, categories: false, size: true, flag: true }
    setMailColumns(cols)
    expect(getMailColumns()).toEqual(cols)
  })

  it("fills a missing key from the defaults (forward-compatible)", () => {
    // Only two keys stored: the rest must fall back to the defaults, not to false.
    document.cookie = `hermex-mail-columns=${encodeURIComponent(JSON.stringify({ size: true, preview: false }))}; Path=/`
    expect(getMailColumns()).toEqual({
      preview: false,
      attachment: true,
      importance: true,
      categories: true,
      size: true,
      flag: false,
    })
  })

  it("falls back to the defaults on a malformed cookie", () => {
    document.cookie = "hermex-mail-columns=not-json; Path=/"
    expect(getMailColumns()).toEqual(defaultMailColumns())
  })
})
