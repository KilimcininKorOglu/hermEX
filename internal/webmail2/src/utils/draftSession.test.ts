import { describe, expect, it, vi } from "vitest"
import { draftSession } from "./draftSession"
import { singleFlight } from "./singleFlight"

// deferred is a promise the test resolves by hand, standing in for a save the
// server has not answered yet.
function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}

describe("draftSession", () => {
  it("makes a send wait for a running save and consume the draft it creates", async () => {
    const session = draftSession(singleFlight())
    const answer = deferred<string>()
    void session.save(() => answer.promise)
    const settled = session.settle()
    answer.resolve("drafts:7")
    expect(await settled).toBe("drafts:7")
  })

  it("stores no draft once a send has begun", async () => {
    const session = draftSession(singleFlight())
    await session.settle()
    const store = vi.fn(async () => "drafts:8")
    expect(await session.save(store)).toBe(false)
    expect(store).not.toHaveBeenCalled()
  })

  it("updates the draft it already saved and saves again after a failed send", async () => {
    const session = draftSession(singleFlight())
    await session.save(async () => "drafts:9")
    await session.settle()
    session.reopen()
    const store = vi.fn(async (id: string | undefined) => id)
    expect(await session.save(store)).toBe(true)
    expect(store).toHaveBeenCalledWith("drafts:9")
  })
})
