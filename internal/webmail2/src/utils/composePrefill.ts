import { sanitizeHTML } from './sanitize'

// ComposePrefill is what the composer fills in from a URL or a stored draft.
export interface ComposePrefill {
  subject?: string
  body?: string
}

// prefillFromParams reads the compose screen's subject/body query parameters.
//
// The body is the quoted original of a reply or forward, which is
// attacker-controlled HTML from whoever sent the mail, and the parameter is
// reachable through a crafted link on its own. The editor renders its value as
// HTML and the backend serves mail bodies unsanitized by design, so this is the
// boundary that has to sanitize, and it lives here rather than inline in the
// component so a regression is a failing test rather than a live XSS.
export function prefillFromParams(params: URLSearchParams): ComposePrefill {
  const out: ComposePrefill = {}
  const subject = params.get('subject')
  if (subject) out.subject = subject
  const body = params.get('body')
  if (body) out.body = sanitizeHTML(body)
  return out
}

// prefillFromDraft sanitizes a stored draft's body before it reaches the editor.
// The draft id comes from the URL, so a crafted link can point the composer at
// any message the account can read, not only one the user wrote.
export function prefillFromDraft(body: string | undefined): string {
  return sanitizeHTML(body || '')
}
