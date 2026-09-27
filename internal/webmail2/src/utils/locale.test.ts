import { describe, it, expect } from 'vitest'
import { resolveLocale } from './locale'

describe('resolveLocale', () => {
  it('takes the user choice first', () => {
    expect(resolveLocale('tr', 'en-US')).toBe('tr')
  })

  it('follows the browser without a choice', () => {
    expect(resolveLocale(null, 'tr-TR')).toBe('tr')
    expect(resolveLocale('', 'TR')).toBe('tr')
  })

  it('falls back to English for a language it does not ship', () => {
    // An unsupported stored choice must not become the interface language.
    expect(resolveLocale('de', 'en-GB')).toBe('en')
    expect(resolveLocale(null, 'de-DE')).toBe('en')
  })
})
