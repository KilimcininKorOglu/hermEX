import { describe, it, expect } from 'vitest'
import { sanitizeHTML, sanitizeEmailBody, sanitizeText, sanitizeClipboard } from './sanitize'

describe('sanitizeHTML', () => {
  it('allows safe HTML tags', () => {
    const input = '<p>Hello <strong>world</strong></p>'
    const result = sanitizeHTML(input)
    expect(result).toContain('<strong>world</strong>')
    expect(result).not.toContain('<script>')
  })

  it('removes script tags', () => {
    const input = '<script>alert("xss")</script><p>Hello</p>'
    const result = sanitizeHTML(input)
    expect(result).not.toContain('<script>')
    expect(result).not.toContain('alert')
    expect(result).toContain('<p>Hello</p>')
  })

  it('removes inline event handlers like onerror', () => {
    const input = '<img src=x onerror=alert(1)>'
    const result = sanitizeHTML(input)
    expect(result).not.toContain('onerror')
  })

  // Assert on the tag NAME, not on a literal '<iframe>'. Every real payload
  // carries attributes, so an exact-tag assertion passes on unsanitized input and
  // the case can only fail for a bare tag nobody sends.
  it('forbids iframes', () => {
    const input = '<iframe src="https://evil.com"></iframe><p>content</p>'
    const result = sanitizeHTML(input)
    expect(result).not.toMatch(/<iframe/i)
    expect(result).not.toContain('evil.com')
    expect(result).toContain('<p>content</p>')
  })

  // The remaining forbidden tags carry attributes too, and none of them was
  // covered at all, so the same regression would have gone equally unnoticed.
  it.each([
    ['object', '<object data="https://evil.com/x.swf"></object>'],
    ['embed', '<embed src="https://evil.com/x.swf">'],
    ['form', '<form action="https://evil.com/steal"><input name="p"></form>'],
    ['script', '<script src="https://evil.com/x.js"></script>'],
  ])('forbids %s even with attributes', (tag, payload) => {
    const result = sanitizeHTML(payload + '<p>content</p>')
    expect(result).not.toMatch(new RegExp('<' + tag, 'i'))
    expect(result).not.toContain('evil.com')
    expect(result).toContain('<p>content</p>')
  })

  // The URI allowlist is what keeps a link in a received message from running
  // script in the origin that holds the session cookie. Its scheme-less branch
  // used to admit every scheme, so these all rendered as live javascript: links.
  it.each([
    ['javascript', '<a href="javascript:alert(1)">click</a>'],
    ['mixed case', '<a href="JaVaScRiPt:alert(1)">click</a>'],
    ['vbscript', '<a href="vbscript:msgbox(1)">click</a>'],
    ['entity-encoded', '<a href="&#106;avascript:alert(1)">click</a>'],
    ['image source', '<img src="javascript:alert(1)">'],
  ])('drops a %s script URL', (_name, payload) => {
    const result = sanitizeHTML(payload)
    expect(result).not.toMatch(/javascript:/i)
    expect(result).not.toMatch(/vbscript:/i)
  })

  // The same body path the reader actually renders, so the guard is proven where
  // untrusted mail is displayed and not only on the raw helper.
  it('drops script URLs from a rendered email body', () => {
    const { html } = sanitizeEmailBody('<a href="javascript:alert(1)">click</a>', true)
    expect(html).not.toMatch(/javascript:/i)
    expect(html).toContain('click')
  })

  // Narrowing the scheme rule must not take the legitimate ones with it: inline
  // images are cid:/data:, and a body full of stripped links would be its own bug.
  it.each([
    ['<a href="https://example.com/x">ok</a>', 'https://example.com/x'],
    ['<a href="mailto:a@b.test">mail</a>', 'mailto:a@b.test'],
    ['<a href="/relative/path">rel</a>', '/relative/path'],
    ['<a href="#anchor">anchor</a>', '#anchor'],
    ['<img src="cid:part1">', 'cid:part1'],
    ['<img src="data:image/png;base64,AAA">', 'data:image/png;base64,AAA'],
  ])('keeps %s', (input, kept) => {
    expect(sanitizeHTML(input)).toContain(kept)
  })

  it('allows links with target attribute', () => {
    const input = '<a href="https://example.com" target="_blank">Link</a>'
    const result = sanitizeHTML(input)
    expect(result).toContain('target="_blank"')
    expect(result).toContain('https://example.com')
  })
})

describe('sanitizeText', () => {
  it('strips all HTML tags', () => {
    const input = '<p>Hello <strong>world</strong></p>'
    const result = sanitizeText(input)
    expect(result).toBe('Hello world')
  })

  it('removes script content', () => {
    const input = '<script>doEvil()</script>Safe text'
    const result = sanitizeText(input)
    expect(result).not.toContain('<script>')
    expect(result).not.toContain('doEvil')
    expect(result).toContain('Safe text')
  })

  it('returns plain text unchanged', () => {
    const input = 'Plain text without HTML'
    const result = sanitizeText(input)
    expect(result).toBe('Plain text without HTML')
  })
})

describe('sanitizeClipboard', () => {
  // The paste path is the one sink the value prop never guards: the browser
  // inserts the fragment itself, so an on-insertion handler runs unless the
  // payload is sanitized first.
  it('strips handlers that fire on insertion', () => {
    const result = sanitizeClipboard('<img src=x onerror="steal()"><svg onload="steal()"></svg>', '')
    expect(result).not.toContain('onerror')
    expect(result).not.toContain('onload')
    expect(result).not.toContain('steal')
  })

  it('keeps the formatting a paste is for', () => {
    const result = sanitizeClipboard('<p>Hello <strong>world</strong></p>', 'Hello world')
    expect(result).toContain('<strong>world</strong>')
  })

  it('escapes a plain-text payload so it cannot introduce markup', () => {
    const result = sanitizeClipboard('', '<img src=x onerror=steal()>')
    expect(result).not.toMatch(/<img/i)
    expect(result).toContain('&lt;img')
  })

  it('keeps line breaks in a plain-text payload', () => {
    expect(sanitizeClipboard('', 'first\r\nsecond\nthird')).toBe('first<br>second<br>third')
  })

  // A whitespace-only text/html flavour is what a plain-text copy leaves behind
  // in some browsers; the text payload must still win there.
  it('falls back to the text payload when the HTML flavour is blank', () => {
    expect(sanitizeClipboard('   ', 'plain')).toBe('plain')
  })
})
