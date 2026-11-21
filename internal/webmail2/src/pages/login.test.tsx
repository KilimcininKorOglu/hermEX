import { describe, it, expect } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { LoginFooter, inkFor } from '@/components/sign-in-badge'

// React only allows act() when the environment declares itself a test one.
;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// footerText renders the login footer for branding and returns its text.
function footerText(branding: Parameters<typeof LoginFooter>[0]['branding']): string {
  const container = document.createElement('div')
  const root = createRoot(container)
  act(() => root.render(<LoginFooter branding={branding} appName="hermEX" />))
  const text = container.textContent ?? ''
  act(() => root.unmount())
  return text
}

describe('LoginFooter', () => {
  it('shows the tenant footer text and no server version', () => {
    // The branding response still carries the version for the About page.
    const branding = {
      app_name: 'Acme Mail', logo_url: '', primary_color: '', footer_text: 'Acme',
      version: '0.2.0 (abc1234)',
    }
    expect(footerText(branding)).toBe('Acme')
  })

  it('falls back to the app name without a footer text', () => {
    expect(footerText(null)).toBe('hermEX')
  })
})

// The band text must stay readable on any colour a tenant picks (WCAG 2.x
// contrast): a light band takes the dark ink, a dark band the light ink.
describe('inkFor', () => {
  it('puts dark ink on a light band and light ink on a dark band', () => {
    expect(inkFor('#ffd400')).toBe('#151413')
    expect(inkFor('#FFFFFF')).toBe('#151413')
    expect(inkFor('#ff0')).toBe('#151413')
    expect(inkFor('#4f46e5')).toBe('#ffffff')
    expect(inkFor('#000000')).toBe('#ffffff')
  })

  it('gives a value it cannot read the default light ink', () => {
    expect(inkFor('red')).toBe('#ffffff')
    expect(inkFor('url(javascript:alert(1))')).toBe('#ffffff')
    expect(inkFor('#12345')).toBe('#ffffff')
    expect(inkFor('')).toBe('#ffffff')
  })
})
