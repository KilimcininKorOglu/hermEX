// PasswordTooShortError is a new password the server refused as shorter than
// its minimum. The minimum is the operator's setting, so the SPA shows the number
// the server sent instead of a copy of its own.
export class PasswordTooShortError extends Error {
  readonly minLength: number

  constructor(minLength: number) {
    super(`the new password must be at least ${minLength} characters`)
    this.name = "PasswordTooShortError"
    this.minLength = minLength
  }
}

// passwordChangeFailure turns a refused password-change response into the error
// the dialogs show: a too-short refusal when the 400 carries a positive integer
// minLength, a plain error with the server's message otherwise.
export function passwordChangeFailure(status: number, body: unknown): Error {
  const fields = typeof body === "object" && body !== null ? (body as Record<string, unknown>) : {}
  const minLength = fields.minLength
  if (status === 400 && typeof minLength === "number" && Number.isInteger(minLength) && minLength > 0) {
    return new PasswordTooShortError(minLength)
  }
  const message = typeof fields.error === "string" ? fields.error : `HTTP ${status}`
  return new Error(message)
}

// passwordsMismatch reports whether the confirmation differs from the new password,
// the one check the dialogs make before sending; the length rule is the server's.
export function passwordsMismatch(next: string, confirm: string): boolean {
  return next !== confirm
}
