import { describe, it, expect, vi, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { I18nProvider } from '@/hooks/useI18n'
import { ThemeProvider } from '@/components/theme-provider'
import api from '@/utils/api'
import { getCookie } from '@/utils/cookies'
import { LoginPrefs } from './login-prefs'

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// jsdom has no matchMedia; the "system" theme asks it for the colour scheme.
window.matchMedia = ((query: string) => ({ matches: false, media: query }) as MediaQueryList) as typeof window.matchMedia

// mount renders the sign-in controls inside the providers the login page has.
function mount(): { root: Root; host: HTMLElement } {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  act(() => root.render(<I18nProvider><ThemeProvider><LoginPrefs /></ThemeProvider></I18nProvider>))
  return { root, host }
}

describe('LoginPrefs', () => {
  let mounted: { root: Root; host: HTMLElement } | null = null
  afterEach(() => {
    act(() => mounted?.root.unmount())
    mounted?.host.remove()
    mounted = null
    document.cookie = 'webmail-theme=; Max-Age=0; Path=/'
    document.cookie = 'hermex-language=; Max-Age=0; Path=/'
    vi.restoreAllMocks()
  })

  it('switches the theme into the cookie without a session to save it to', () => {
    const save = vi.spyOn(api, 'setUserPrefs')
    mounted = mount()
    const toggle = mounted.host.querySelectorAll('button')[1]
    act(() => toggle.click())
    expect(getCookie('webmail-theme')).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(save).not.toHaveBeenCalled()
  })

  it('switches the language into the cookie without a session to save it to', async () => {
    const save = vi.spyOn(api, 'setUserPrefs')
    mounted = mount()
    const trigger = mounted.host.querySelectorAll('button')[0]
    await act(async () => {
      trigger.focus()
      trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    })
    const turkish = Array.from(document.querySelectorAll('[role=menuitem]')).find((el) => el.textContent?.includes('Türkçe'))
    expect(turkish).toBeDefined()
    await act(async () => (turkish as HTMLElement).click())
    expect(getCookie('hermex-language')).toBe('tr')
    expect(document.documentElement.lang).toBe('tr')
    expect(save).not.toHaveBeenCalled()
  })
})
