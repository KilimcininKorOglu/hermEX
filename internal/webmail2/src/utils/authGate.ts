// Gate decides which screen a signed-in user must pass before reaching the
// mailbox. The order matters and is enforced on the server as well: a session
// that has not finished logging in may not act on the account at all, so the
// second factor comes before the forced password change, which in turn comes
// before first-run onboarding.

export interface GateUser {
  secondFactorRequired?: boolean
  mustChangePassword?: boolean
  onboarded?: boolean
}

export type Gate = "/second-factor" | "/force-password" | "/onboarding" | null

// firstGate returns the route the user must be sent to, or null when nothing
// stands between them and their mail.
//
// onboarding fires ONLY when the flag is explicitly false. A login fallback that
// could not read /auth/me leaves it undefined, and treating that as "not
// onboarded" would trap the user on the first-run screen.
export function firstGate(user: GateUser | null): Gate {
  if (!user) return null
  if (user.secondFactorRequired) return "/second-factor"
  if (user.mustChangePassword) return "/force-password"
  if (user.onboarded === false) return "/onboarding"
  return null
}
