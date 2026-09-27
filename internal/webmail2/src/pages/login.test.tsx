import { describe, it, expect } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { LoginFooter } from './login'

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
