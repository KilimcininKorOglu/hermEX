import { describe, it, expect } from 'vitest'
import { adoptions, type BrowserChoices } from './prefs-sync'

const noChoices: BrowserChoices = { theme: null, locale: null, bannerDismissed: false }

describe('adoptions', () => {
  it('adopts the choices this browser made before the record stored them', () => {
    const browser: BrowserChoices = { theme: 'light', locale: 'tr', bannerDismissed: true }
    expect(adoptions({ theme: '', locale: '', showWelcomeBanner: true }, browser, 'dark'))
      .toEqual({ theme: 'dark', locale: 'tr', show_welcome_banner: false })
  })

  it('takes the theme cookie when the appearance record could not be read', () => {
    expect(adoptions({ theme: '' }, { ...noChoices, theme: 'light' }, undefined)).toEqual({ theme: 'light' })
  })

  it('keeps every value the record already holds', () => {
    const browser: BrowserChoices = { theme: 'light', locale: 'en', bannerDismissed: true }
    expect(adoptions({ theme: 'dark', locale: 'tr', showWelcomeBanner: false }, browser, 'light')).toEqual({})
  })

  it('ignores a cookie language the SPA does not ship', () => {
    expect(adoptions({ locale: '' }, { ...noChoices, locale: 'de' }, undefined)).toEqual({})
  })
})
