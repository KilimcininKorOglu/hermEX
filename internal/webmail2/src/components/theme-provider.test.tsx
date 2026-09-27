import { describe, it, expect, vi, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { I18nProvider } from '@/hooks/useI18n'
import api from '@/utils/api'
import { getCookie } from '@/utils/cookies'
import { ThemeProvider, useTheme, type Theme } from './theme-provider'

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// jsdom has no matchMedia; the "system" theme asks it for the colour scheme.
window.matchMedia = ((query: string) => ({ matches: false, media: query }) as MediaQueryList) as typeof window.matchMedia

// mountTheme renders the provider and hands back its live state.
function mountTheme(): { state: () => ReturnType<typeof useTheme>; root: Root } {
  let current: ReturnType<typeof useTheme> | null = null
  function Grab() {
    current = useTheme()
    return null
  }
  const root = createRoot(document.createElement('div'))
  act(() => root.render(<I18nProvider><ThemeProvider storageKey="test-theme"><Grab /></ThemeProvider></I18nProvider>))
  return { state: () => current as unknown as ReturnType<typeof useTheme>, root }
}

describe('ThemeProvider', () => {
  let root: Root | null = null
  afterEach(() => {
    act(() => root?.unmount())
    root = null
    document.cookie = 'test-theme=; Max-Age=0; Path=/'
    vi.restoreAllMocks()
  })

  it('stores a chosen theme in the users record and the cookie', async () => {
    const save = vi.spyOn(api, 'setUserPrefs').mockResolvedValue({ theme: 'dark', locale: '', show_welcome_banner: true })
    const mounted = mountTheme()
    root = mounted.root
    await act(async () => mounted.state().setTheme('dark'))
    expect(save).toHaveBeenCalledWith({ theme: 'dark' })
    expect(mounted.state().theme).toBe('dark')
    expect(getCookie('test-theme')).toBe('dark')
  })

  it('puts the previous theme back when the save fails', async () => {
    vi.spyOn(api, 'setUserPrefs').mockRejectedValue(new Error('HTTP 500'))
    const mounted = mountTheme()
    root = mounted.root
    const before: Theme = mounted.state().theme
    await act(async () => mounted.state().setTheme(before === 'dark' ? 'light' : 'dark'))
    expect(mounted.state().theme).toBe(before)
    expect(getCookie('test-theme')).toBe(before)
  })

  it('applies a stored theme without saving it again', () => {
    const save = vi.spyOn(api, 'setUserPrefs')
    const mounted = mountTheme()
    root = mounted.root
    act(() => mounted.state().applyStoredTheme('light'))
    expect(mounted.state().theme).toBe('light')
    expect(save).not.toHaveBeenCalled()
  })
})
