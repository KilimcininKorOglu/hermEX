import { describe, it, expect } from "vitest"
import { firstGate } from "./authGate"

describe("firstGate", () => {
  it("lets a fully signed-in user through", () => {
    expect(firstGate({ onboarded: true })).toBeNull()
    expect(firstGate(null)).toBeNull()
  })

  it("puts the second factor before everything else", () => {
    // The one that matters: a session that has cleared only the password must
    // not be routed to the password-change or onboarding screen, because it may
    // not act on the account at all. The API answers 403 for both.
    expect(
      firstGate({ secondFactorRequired: true, mustChangePassword: true, onboarded: false }),
    ).toBe("/second-factor")
  })

  it("puts the forced password change before onboarding", () => {
    expect(firstGate({ mustChangePassword: true, onboarded: false })).toBe("/force-password")
  })

  it("sends a fresh account to onboarding", () => {
    expect(firstGate({ onboarded: false })).toBe("/onboarding")
  })

  it("does not trap a user whose onboarding state is unknown", () => {
    // A login fallback that could not read /auth/me leaves the flag undefined.
    expect(firstGate({})).toBeNull()
  })
})
