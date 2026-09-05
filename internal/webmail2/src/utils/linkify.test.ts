import { describe, it, expect } from 'vitest'
import { linkifyHTML } from './linkify'
import { sanitizeHTML } from './sanitize'

describe('linkifyHTML', () => {
  it('links an absolute URL', () => {
    expect(linkifyHTML('see https://example.com now')).toBe(
      'see <a href="https://example.com">https://example.com</a> now',
    )
  })

  it('links a www host over https', () => {
    expect(linkifyHTML('see www.example.com')).toBe(
      'see <a href="https://www.example.com">www.example.com</a>',
    )
  })

  it('links an e-mail address as mailto', () => {
    expect(linkifyHTML('write to bob@example.com')).toBe(
      'write to <a href="mailto:bob@example.com">bob@example.com</a>',
    )
  })

  it('leaves trailing sentence punctuation outside the link', () => {
    expect(linkifyHTML('go to https://example.com.')).toBe(
      'go to <a href="https://example.com">https://example.com</a>.',
    )
    expect(linkifyHTML('(https://example.com)')).toBe(
      '(<a href="https://example.com">https://example.com</a>)',
    )
  })

  it('does not touch a URL that is already a link', () => {
    const html = '<a href="https://example.com">https://example.com</a>'
    expect(linkifyHTML(html)).toBe(html)
  })

  it('does not match inside an attribute', () => {
    // A regular expression over the markup would match the href and the src and
    // produce an anchor inside an attribute value.
    const html = '<img src="https://example.com/a.png">'
    expect(linkifyHTML(html)).toBe(html)
  })

  it('links several tokens in one text node', () => {
    expect(linkifyHTML('a https://one.test b bob@two.test c')).toBe(
      'a <a href="https://one.test">https://one.test</a> b ' +
        '<a href="mailto:bob@two.test">bob@two.test</a> c',
    )
  })

  it('leaves text with nothing to link alone', () => {
    expect(linkifyHTML('<p>plain body, 12:30, C:\\path</p>')).toBe('<p>plain body, 12:30, C:\\path</p>')
  })

  it('produces only schemes the sanitizer admits', () => {
    // A linkified body is sanitized again before it is stored or sent, so an
    // anchor this produces must survive that pass with its href intact.
    const linked = linkifyHTML('https://example.com and bob@example.com and www.example.com')
    expect(sanitizeHTML(linked)).toBe(linked)
  })

  it('never links a javascript scheme', () => {
    const out = linkifyHTML('javascript:alert(1) and jAvAsCrIpT:alert(2)')
    expect(out).not.toContain('<a')
  })
})
