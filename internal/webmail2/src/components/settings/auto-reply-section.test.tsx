import { describe, it, expect, vi, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { I18nProvider } from '@/hooks/useI18n'
import api, { type VacationAutoReply } from '@/utils/api'
import { AutoReplySection } from './auto-reply-section'

// React only allows act() when the environment declares itself a test one.
;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// mount renders the section and waits for its load to settle.
async function mount() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <I18nProvider>
        <AutoReplySection />
      </I18nProvider>,
    )
  })
  const cleanup = () => {
    act(() => root.unmount())
    container.remove()
  }
  return { container, cleanup }
}

describe('AutoReplySection', () => {
  afterEach(() => vi.restoreAllMocks())

  it('hides the form when the stored reply cannot be loaded', async () => {
    vi.spyOn(api, 'getVacation').mockRejectedValue(new Error('500'))
    const save = vi.spyOn(api, 'setVacation')
    const { container, cleanup } = await mount()
    // An empty form here would overwrite the stored reply on its first save.
    expect(container.querySelector('[role="alert"]')).not.toBeNull()
    expect(container.querySelector('#vacation-message')).toBeNull()
    expect(save).not.toHaveBeenCalled()
    cleanup()
  })

  it('shows the stored reply when it loads', async () => {
    const stored: VacationAutoReply = { enabled: true, subject: '', message: 'away', audience: 'all' }
    vi.spyOn(api, 'getVacation').mockResolvedValue(stored)
    const { container, cleanup } = await mount()
    expect(container.querySelector('[role="alert"]')).toBeNull()
    expect((container.querySelector('#vacation-message') as HTMLTextAreaElement).value).toBe('away')
    cleanup()
  })
})
