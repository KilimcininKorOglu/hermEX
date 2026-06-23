import DOMPurify from 'isomorphic-dompurify'

// Module flags driving the shared afterSanitizeAttributes hook. They are set just
// around a sanitizeEmailBody() call so remote-image blocking is opt-in per render.
let blockRemoteImages = false
let remoteWasBlocked = false

// Single afterSanitizeAttributes hook: injects rel="noopener noreferrer" on
// target="_blank" links (CWE-1022), and — when blocking is on — neutralizes
// remote images (http/https or protocol-relative) so tracking pixels do not load.
// Inline images (cid:/data:) are kept untouched.
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  const el = node as Element
  if (el.tagName === 'A' && el.getAttribute('target') === '_blank') {
    el.setAttribute('rel', 'noopener noreferrer')
  }
  if (blockRemoteImages && el.tagName === 'IMG') {
    const src = el.getAttribute('src') || ''
    if (/^(?:https?:)?\/\//i.test(src)) {
      remoteWasBlocked = true
      el.removeAttribute('src')
    }
  }
})

const EMAIL_SANITIZE_CONFIG = {
  ALLOWED_TAGS: [
    'html', 'body', 'head', 'style',
    'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
    'p', 'br', 'hr',
    'ul', 'ol', 'li', 'dl', 'dt', 'dd',
    'blockquote', 'pre', 'code',
    'a', 'img', 'figure', 'figcaption',
    'table', 'thead', 'tbody', 'tfoot', 'tr', 'th', 'td',
    'div', 'span', 'section', 'article', 'header', 'footer', 'nav', 'aside', 'main',
    'strong', 'b', 'em', 'i', 'u', 's', 'strike', 'del', 'ins',
    'sup', 'sub',
    'q', 'cite', 'abbr', 'acronym', 'mark',
    'details', 'summary',
  ],
  ALLOWED_ATTR: [
    'href', 'src', 'alt', 'title', 'class', 'id',
    'width', 'height', 'colspan', 'rowspan',
    'target', 'rel',
    'style',
  ],
  ALLOW_DATA_ATTR: false,
  ADD_ATTR: ['target'],
  FORBID_TAGS: ['script', 'style', 'iframe', 'form', 'input', 'button', 'object', 'embed'],
  FORBID_ATTR: ['onerror', 'onload', 'onclick', 'onmouseover', 'onfocus', 'onblur', 'onchange', 'onsubmit'],
  // Keep cid:/data: images for inline rendering; drop javascript: and the like.
  ALLOWED_URI_REGEXP: /^(?:(?:https?|mailto|tel|cid|data):|[^a-z]|[a-z+\-.]+(?:[^a-z+\-.]|$))/i,
}

/**
 * Sanitizes HTML content to prevent XSS attacks (strict DOMPurify config).
 */
export function sanitizeHTML(dirty: string): string {
  return DOMPurify.sanitize(dirty, EMAIL_SANITIZE_CONFIG)
}

/**
 * Sanitizes an email body and, when blockRemote is true, neutralizes remote
 * images so tracking pixels do not load on open. blockedRemote reports whether
 * anything was blocked, so the reader can offer a "show images" affordance.
 */
export function sanitizeEmailBody(dirty: string, blockRemote: boolean): { html: string; blockedRemote: boolean } {
  blockRemoteImages = blockRemote
  remoteWasBlocked = false
  try {
    const html = DOMPurify.sanitize(dirty, EMAIL_SANITIZE_CONFIG)
    return { html, blockedRemote: remoteWasBlocked }
  } finally {
    blockRemoteImages = false
  }
}

/**
 * Sanitizes plain text for safe display (strips all HTML).
 */
export function sanitizeText(dirty: string): string {
  return DOMPurify.sanitize(dirty, {
    ALLOWED_TAGS: [],
    ALLOWED_ATTR: [],
  })
}
