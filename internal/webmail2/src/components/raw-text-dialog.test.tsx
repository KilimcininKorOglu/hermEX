import { describe, it, expect, vi, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { I18nProvider } from '@/hooks/useI18n'
import api from '@/utils/api'
import { MAX_SHOWN_CHARS, RawTextDialog, type RawTextKind } from './raw-text-dialog'

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// mountDialog renders an open dialog for kind and waits for its load to settle.
async function mountDialog(kind: RawTextKind): Promise<{ root: Root; container: HTMLElement }> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <I18nProvider>
        <RawTextDialog open onOpenChange={() => {}} kind={kind} id="inbox:7" baseName="Hello" />
      </I18nProvider>,
    )
  })
  return { root, container }
}

// dialogText is what the open dialog shows.
function dialogText(): string {
  return document.querySelector('[role="dialog"]')?.textContent ?? ''
}

describe('RawTextDialog', () => {
  let mounted: { root: Root; container: HTMLElement } | null = null
  afterEach(() => {
    if (mounted) {
      act(() => mounted?.root.unmount())
      mounted.container.remove()
      mounted = null
    }
    vi.restoreAllMocks()
  })

  it('shows the header block in the page instead of a new tab', async () => {
    const headers = vi.spyOn(api, 'getMessageHeaders').mockResolvedValue('Subject: Hello\r\nFrom: bob@hermex.test\r\n')
    const raw = vi.spyOn(api, 'getMessageRaw')
    const open = vi.spyOn(window, 'open')
    mounted = await mountDialog('headers')
    expect(headers).toHaveBeenCalledWith('inbox:7')
    expect(raw).not.toHaveBeenCalled()
    expect(open).not.toHaveBeenCalled()
    expect(dialogText()).toContain('From: bob@hermex.test')
  })

  it('shows the source and bounds what it renders', async () => {
    const big = 'x'.repeat(MAX_SHOWN_CHARS + 10)
    vi.spyOn(api, 'getMessageRaw').mockResolvedValue(big)
    mounted = await mountDialog('source')
    const pre = document.querySelector('[role="dialog"] pre')
    expect(pre?.textContent).toHaveLength(MAX_SHOWN_CHARS)
    expect(dialogText()).toMatch(/1 MB|rawTruncated/)
  })

  it('reports a failed load rather than an empty text', async () => {
    vi.spyOn(api, 'getMessageRaw').mockRejectedValue(new Error('HTTP 404'))
    mounted = await mountDialog('source')
    expect(document.querySelector('[role="dialog"] pre')).toBeNull()
    expect(dialogText()).toMatch(/Could not load|rawLoadFailed/)
  })
})
