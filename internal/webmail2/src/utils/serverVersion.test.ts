import { afterEach, describe, expect, it, vi } from "vitest"
import { fetchServerVersion, shownVersion } from "./serverVersion"

describe("shownVersion", () => {
  it("shows the release and the commit the server reports", () => {
    expect(shownVersion("0.1.0 (adeff8a)")).toBe("0.1.0 (adeff8a)")
    expect(shownVersion("0.1.0-dev+adeff8a-dirty")).toBe("0.1.0-dev+adeff8a-dirty")
  })

  it("hides an unstamped build and anything that is not a version string", () => {
    expect(shownVersion("unknown")).toBe("")
    expect(shownVersion(undefined)).toBe("")
    expect(shownVersion(42)).toBe("")
  })
})

describe("fetchServerVersion", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("reads the version from the branding endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ version: "0.1.0 (adeff8a)" })))
    vi.stubGlobal("fetch", fetchMock)
    await expect(fetchServerVersion()).resolves.toBe("0.1.0 (adeff8a)")
    expect(fetchMock).toHaveBeenCalledWith(`${window.location.origin}/api/v1/branding`)
  })

  it("reports a failed request instead of an empty version", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status: 503 })))
    await expect(fetchServerVersion()).rejects.toThrow("503")
  })
})
