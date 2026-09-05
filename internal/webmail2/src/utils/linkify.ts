/**
 * Auto-linkification for the compose editor.
 *
 * A bare URL or e-mail address typed or pasted into the body stays plain text
 * unless the user reaches for the link tool, which is not what any mail client
 * does. This turns those into anchors.
 *
 * It works on the DOM rather than on the HTML string: a regular expression over
 * markup would happily match inside an href or a src attribute and produce nested
 * or broken anchors. Walking text nodes cannot see attributes at all, and a node
 * already inside an <a> is skipped so nothing is wrapped twice.
 *
 * Only the schemes the sanitizer admits are produced, so a linkified body survives
 * sanitizeHTML unchanged. Anything else is left as text.
 */

// URL_PATTERN matches an absolute http(s) URL, a www-prefixed host, or an e-mail
// address. The alternatives are ordered longest-first so "www." inside an
// "https://www." match is not taken on its own.
const URL_PATTERN =
  /(https?:\/\/[^\s<>"']+|www\.[^\s<>"']+|[\w!#$%&'*+/=?^`{|}~.-]+@[\w-]+(?:\.[\w-]+)+)/gi

// TRAILING_PUNCTUATION is stripped from the end of a match and left as text. A URL
// at the end of a sentence must not swallow the full stop, and a URL in
// parentheses must not swallow the closing one.
const TRAILING_PUNCTUATION = /[.,;:!?)\]}'"]+$/

/** hrefFor returns the href a matched token should point at, or null to leave it. */
function hrefFor(token: string): string | null {
  if (/^https?:\/\//i.test(token)) return token
  if (/^www\./i.test(token)) return `https://${token}`
  if (token.includes('@')) return `mailto:${token}`
  return null
}

/** insideAnchor reports whether a node already sits inside a link. */
function insideAnchor(node: Node): boolean {
  for (let p = node.parentNode; p; p = p.parentNode) {
    if ((p as Element).nodeName === 'A') return true
  }
  return false
}

/**
 * linkifyNode replaces every bare URL and e-mail address in the element's text
 * nodes with an anchor, in place.
 */
export function linkifyNode(root: Element | DocumentFragment): void {
  const doc = root.ownerDocument ?? document
  const walker = doc.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  const targets: Text[] = []
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const text = n as Text
    if (!insideAnchor(text) && URL_PATTERN.test(text.data)) targets.push(text)
    URL_PATTERN.lastIndex = 0
  }

  for (const text of targets) {
    const frag = doc.createDocumentFragment()
    let last = 0
    for (const m of text.data.matchAll(URL_PATTERN)) {
      const start = m.index ?? 0
      let token = m[0]
      const trailing = TRAILING_PUNCTUATION.exec(token)
      if (trailing) token = token.slice(0, token.length - trailing[0].length)
      const href = token ? hrefFor(token) : null
      if (!href) continue

      if (start > last) frag.appendChild(doc.createTextNode(text.data.slice(last, start)))
      const a = doc.createElement('a')
      a.setAttribute('href', href)
      a.textContent = token
      frag.appendChild(a)
      last = start + token.length
    }
    if (last === 0) continue
    if (last < text.data.length) frag.appendChild(doc.createTextNode(text.data.slice(last)))
    text.parentNode?.replaceChild(frag, text)
  }
}

/** linkifyHTML returns the fragment with its bare URLs and addresses linkified. */
export function linkifyHTML(html: string): string {
  const tpl = document.createElement('template')
  tpl.innerHTML = html
  linkifyNode(tpl.content)
  return tpl.innerHTML
}
