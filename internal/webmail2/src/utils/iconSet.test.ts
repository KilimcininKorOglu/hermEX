import { describe, it, expect, afterEach } from "vitest"
import { applyIconSet } from "./iconSet"

describe("applyIconSet", () => {
  afterEach(() => {
    delete document.documentElement.dataset.iconset
  })

  it("reflects a known set onto <html>", () => {
    applyIconSet("classic")
    expect(document.documentElement.dataset.iconset).toBe("classic")
    applyIconSet("breeze")
    expect(document.documentElement.dataset.iconset).toBe("breeze")
  })

  it("falls back to breeze for an unknown value", () => {
    applyIconSet("flat")
    expect(document.documentElement.dataset.iconset).toBe("breeze")
    applyIconSet("")
    expect(document.documentElement.dataset.iconset).toBe("breeze")
  })
})
