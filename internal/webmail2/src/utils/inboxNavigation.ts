import { getCookie, setCookie } from "@/utils/cookies"

// Inbox navigation preference (reference SettingsInboxNavigationWidget): the
// message list either paginates (prev/next over fixed pages) or scrolls
// infinitely (an IntersectionObserver appends the next block at the bottom).
// The DB stays the source of truth; this cookie mirror lets MailboxContext pick
// the mode + page size synchronously before its first fetch, without waiting on
// an async settings load.
export type InboxNavMode = "pagination" | "infinite"

const MODE_COOKIE = "hermex-inbox-nav-mode"
const SIZE_COOKIE = "hermex-inbox-page-size"

// Page-size bounds match the reference widget (10..200 rows/block); the default
// window is 50.
export const MIN_PAGE_SIZE = 10
export const MAX_PAGE_SIZE = 200
export const DEFAULT_PAGE_SIZE = 50

// clampPageSize coerces any input to an integer within [MIN, MAX], falling back
// to the default when it is not a finite number.
export function clampPageSize(n: number): number {
  if (!Number.isFinite(n)) return DEFAULT_PAGE_SIZE
  return Math.min(MAX_PAGE_SIZE, Math.max(MIN_PAGE_SIZE, Math.round(n)))
}

// getInboxNavMode / getInboxPageSize read the cached preference synchronously,
// defaulting to pagination at DEFAULT_PAGE_SIZE.
export function getInboxNavMode(): InboxNavMode {
  return getCookie(MODE_COOKIE) === "infinite" ? "infinite" : "pagination"
}
export function getInboxPageSize(): number {
  const raw = getCookie(SIZE_COOKIE)
  return raw ? clampPageSize(Number(raw)) : DEFAULT_PAGE_SIZE
}

// setInboxNavigation mirrors the chosen mode + page size to cookies and notifies
// listeners (MailboxContext) so a change in Settings takes effect without a
// reload.
export function setInboxNavigation(mode: InboxNavMode, pageSize: number): void {
  setCookie(MODE_COOKIE, mode)
  setCookie(SIZE_COOKIE, String(clampPageSize(pageSize)))
  document.dispatchEvent(new CustomEvent("inbox-nav-changed"))
}
