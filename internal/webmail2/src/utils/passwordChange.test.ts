import { describe, expect, it } from "vitest"
import { PasswordTooShortError, passwordChangeFailure, passwordsMismatch } from "./passwordChange"

describe("passwordChangeFailure", () => {
  it("reads the server's minimum from a too-short refusal", () => {
    const err = passwordChangeFailure(400, { error: "the new password must be at least 12 characters", minLength: 12 })
    expect(err).toBeInstanceOf(PasswordTooShortError)
    expect((err as PasswordTooShortError).minLength).toBe(12)
  })

  it("does not read a minimum from a refusal of another kind", () => {
    const err = passwordChangeFailure(400, { error: "your current password is incorrect" })
    expect(err).not.toBeInstanceOf(PasswordTooShortError)
    expect(err.message).toBe("your current password is incorrect")
  })

  it("ignores a minLength that is not a positive whole number", () => {
    for (const minLength of ["12", 0, -4, 7.5, null]) {
      expect(passwordChangeFailure(400, { minLength })).not.toBeInstanceOf(PasswordTooShortError)
    }
  })

  it("ignores a minLength on a status other than 400", () => {
    expect(passwordChangeFailure(500, { minLength: 12 })).not.toBeInstanceOf(PasswordTooShortError)
  })

  it("falls back to the status when the body is not an object", () => {
    expect(passwordChangeFailure(502, "Bad Gateway").message).toBe("HTTP 502")
    expect(passwordChangeFailure(500, null).message).toBe("HTTP 500")
  })
})

describe("passwordsMismatch", () => {
  it("flags a confirmation that differs", () => {
    expect(passwordsMismatch("abcdefgh", "abcdefgi")).toBe(true)
    expect(passwordsMismatch("abcdefgh", "abcdefgh")).toBe(false)
  })
})
