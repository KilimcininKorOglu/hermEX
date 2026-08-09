import { describe, it, expect } from 'vitest'
import { prefillFromDraft, prefillFromParams } from './composePrefill'

// The composer's prefill is a live XSS boundary: the body parameter carries the
// quoted original of a reply or forward (attacker-controlled HTML from any
// sender), the link itself can be crafted, and the editor renders the value as
// HTML. These cases are the ones that must never reach it.
describe('prefillFromParams', () => {
  const bodyOf = (raw: string) => prefillFromParams(new URLSearchParams({ body: raw })).body ?? ''

  it('strips an event handler from a quoted image', () => {
    const out = bodyOf('<img src=x onerror="alert(1)">')
    expect(out).not.toContain('onerror')
    expect(out).not.toContain('alert(1)')
  })

  it('strips a script element', () => {
    expect(bodyOf('<p>hi</p><script>alert(1)</script>')).not.toContain('<script')
  })

  it('drops a javascript: link, including mixed case and entity-encoded forms', () => {
    for (const href of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', '&#106;avascript:alert(1)']) {
      const out = bodyOf(`<a href="${href}">click</a>`)
      expect(out.toLowerCase()).not.toContain('javascript:')
    }
  })

  it('keeps the legitimate quoted content', () => {
    const out = bodyOf('<p>Hello <b>there</b></p><a href="https://example.com">link</a>')
    expect(out).toContain('Hello')
    expect(out).toContain('<b>there</b>')
    expect(out).toContain('https://example.com')
  })

  it('passes the subject through and omits absent fields', () => {
    const prefill = prefillFromParams(new URLSearchParams({ subject: 'Re: lunch' }))
    expect(prefill.subject).toBe('Re: lunch')
    expect(prefill.body).toBeUndefined()
  })
})

describe('prefillFromDraft', () => {
  it('sanitizes a stored draft, which a crafted link can point at any readable message', () => {
    expect(prefillFromDraft('<img src=x onerror="alert(1)">')).not.toContain('onerror')
  })

  it('turns a missing body into an empty string rather than undefined', () => {
    expect(prefillFromDraft(undefined)).toBe('')
  })
})
