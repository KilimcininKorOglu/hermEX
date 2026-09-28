import { describe, it, expect, vi, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { I18nProvider } from '@/hooks/useI18n'
import api from '@/utils/api'
import { ReadReceiptBanner } from './read-receipt-banner'

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// mountBanner renders the prompt for one message and returns what it rendered.
async function mountBanner(onAnswered: () => void): Promise<{ root: Root; container: HTMLElement }> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <I18nProvider>
        <ReadReceiptBanner id="inbox:7" onAnswered={onAnswered} />
      </I18nProvider>,
    )
  })
  return { root, container }
}

// buttons returns the prompt's decline and send buttons, in that order.
function buttons(container: HTMLElement): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll('button'))
}

describe('ReadReceiptBanner', () => {
  let mounted: { root: Root; container: HTMLElement } | null = null
  afterEach(() => {
    if (mounted) {
      act(() => mounted?.root.unmount())
      mounted.container.remove()
      mounted = null
    }
    vi.restoreAllMocks()
  })

  it('sends the receipt once, however fast the reader clicks', async () => {
    let settle: () => void = () => {}
    const answer = vi.spyOn(api, 'answerReadReceipt').mockImplementation(() => new Promise<void>((r) => { settle = r }))
    const onAnswered = vi.fn()
    mounted = await mountBanner(onAnswered)
    const send = buttons(mounted.container)[1]
    await act(async () => {
      send.click()
      send.click()
    })
    await act(async () => settle())
    expect(answer).toHaveBeenCalledTimes(1)
    expect(answer).toHaveBeenCalledWith('inbox:7', true)
    expect(onAnswered).toHaveBeenCalledTimes(1)
  })

  it('declines without sending', async () => {
    const answer = vi.spyOn(api, 'answerReadReceipt').mockResolvedValue()
    const onAnswered = vi.fn()
    mounted = await mountBanner(onAnswered)
    await act(async () => buttons(mounted!.container)[0].click())
    expect(answer).toHaveBeenCalledWith('inbox:7', false)
    expect(onAnswered).toHaveBeenCalledTimes(1)
  })

  it('keeps the prompt when the server did not keep the answer', async () => {
    vi.spyOn(api, 'answerReadReceipt').mockRejectedValue(new Error('down'))
    const onAnswered = vi.fn()
    mounted = await mountBanner(onAnswered)
    await act(async () => buttons(mounted!.container)[1].click())
    expect(onAnswered).not.toHaveBeenCalled()
  })
})
