import { getCookie, setCookie } from "@/utils/cookies"
import type { MailListColumns } from "@/utils/api"

// Message-list column visibility (reference MailGridColumnModel). The DB stays
// the source of truth; this cookie mirror lets the inbox read the chosen set
// synchronously on first paint without an async settings fetch.
const COOKIE = "hermex-mail-columns"

// defaultMailColumns matches the server default: show the preview snippet,
// attachment + importance icons, and category badges; hide size and the flag.
export function defaultMailColumns(): MailListColumns {
  return { preview: true, attachment: true, importance: true, categories: true, size: false, flag: false }
}

// getMailColumns reads the cached column set synchronously, falling back to the
// defaults when the cookie is absent or malformed.
export function getMailColumns(): MailListColumns {
  const raw = getCookie(COOKIE)
  if (!raw) return defaultMailColumns()
  try {
    const p = JSON.parse(raw) as Partial<MailListColumns>
    const d = defaultMailColumns()
    return {
      preview: typeof p.preview === "boolean" ? p.preview : d.preview,
      attachment: typeof p.attachment === "boolean" ? p.attachment : d.attachment,
      importance: typeof p.importance === "boolean" ? p.importance : d.importance,
      categories: typeof p.categories === "boolean" ? p.categories : d.categories,
      size: typeof p.size === "boolean" ? p.size : d.size,
      flag: typeof p.flag === "boolean" ? p.flag : d.flag,
    }
  } catch {
    return defaultMailColumns()
  }
}

// setMailColumns mirrors the chosen set to the cookie and notifies listeners
// (the inbox) so a change in Settings takes effect without a reload.
export function setMailColumns(cols: MailListColumns): void {
  setCookie(COOKIE, JSON.stringify(cols))
  document.dispatchEvent(new CustomEvent("mail-columns-changed"))
}
