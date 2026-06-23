// Cookie-backed client storage. The web UI must NOT use localStorage/
// sessionStorage (see .project/AGENT_DIRECTIVES.md §8.1); client-readable
// preferences (language, theme, timezone, session marker, recent searches)
// live in cookies. The auth JWT itself stays in a separate HttpOnly cookie the
// client cannot read — these helpers are for non-sensitive readable state only.

// getCookie returns the decoded value of a cookie, or null when absent.
export function getCookie(name: string): string | null {
  const prefix = name + "="
  for (const part of document.cookie ? document.cookie.split("; ") : []) {
    if (part.startsWith(prefix)) {
      try {
        return decodeURIComponent(part.slice(prefix.length))
      } catch {
        return part.slice(prefix.length)
      }
    }
  }
  return null
}

// setCookie writes a client-readable preference cookie. Path=/ so it applies
// app-wide, SameSite=Lax, and Secure when served over HTTPS. Default lifetime
// is one year (preferences should persist like the old localStorage values).
export function setCookie(name: string, value: string, days = 365): void {
  const maxAge = Math.floor(days * 24 * 60 * 60)
  const secure = typeof location !== "undefined" && location.protocol === "https:" ? "; Secure" : ""
  document.cookie = `${name}=${encodeURIComponent(value)}; Path=/; Max-Age=${maxAge}; SameSite=Lax${secure}`
}

// deleteCookie expires a cookie immediately.
export function deleteCookie(name: string): void {
  const secure = typeof location !== "undefined" && location.protocol === "https:" ? "; Secure" : ""
  document.cookie = `${name}=; Path=/; Max-Age=0; SameSite=Lax${secure}`
}
