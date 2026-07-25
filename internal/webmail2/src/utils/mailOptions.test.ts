import { describe, it, expect } from "vitest"
import { mailOptionsActive, MailOptionsState } from "./mailOptions"

const base: MailOptionsState = {
  importance: "normal",
  sensitivity: "normal",
  requestReadReceipt: false,
  requestDeliveryReceipt: false,
}

describe("mailOptionsActive", () => {
  it("is inactive when everything is at its default", () => {
    expect(mailOptionsActive(base)).toBe(false)
  })

  it("is active when importance is not normal", () => {
    expect(mailOptionsActive({ ...base, importance: "high" })).toBe(true)
    expect(mailOptionsActive({ ...base, importance: "low" })).toBe(true)
  })

  it("is active when sensitivity is not normal", () => {
    expect(mailOptionsActive({ ...base, sensitivity: "confidential" })).toBe(true)
    expect(mailOptionsActive({ ...base, sensitivity: "private" })).toBe(true)
  })

  it("is active when a tracking receipt is requested", () => {
    expect(mailOptionsActive({ ...base, requestReadReceipt: true })).toBe(true)
    expect(mailOptionsActive({ ...base, requestDeliveryReceipt: true })).toBe(true)
  })
})
