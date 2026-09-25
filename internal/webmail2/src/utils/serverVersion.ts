// The version the server reports is the one release number the whole product
// carries (the repository's VERSION file plus the commit it was built from). The
// SPA shows that value and never a number of its own, so the webmail and the
// binaries serving it cannot claim two different releases.

// shownVersion returns the server's version for display. An unstamped binary
// reports "unknown", which says nothing useful to a user, so it is left out
// entirely rather than rendered as a non-answer.
export function shownVersion(raw: unknown): string {
  if (typeof raw !== "string") return ""
  const v = raw.trim()
  return v === "unknown" ? "" : v
}

// fetchServerVersion reads the version from the public branding endpoint.
export async function fetchServerVersion(): Promise<string> {
  const r = await fetch(`${window.location.origin}/api/v1/branding`)
  if (!r.ok) throw new Error(`branding request failed: ${r.status}`)
  const b: { version?: unknown } = await r.json()
  return shownVersion(b.version)
}
