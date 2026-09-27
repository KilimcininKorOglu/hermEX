import { Component, type ErrorInfo, type ReactNode } from 'react'
import { describeError } from '@/utils/errorlog'
import { getCookie } from '@/utils/cookies'
import { resolveLocale } from '@/utils/locale'

interface Props {
  children: ReactNode
}

interface State {
  failed: boolean
}

// fallbackText is the recovery screen in every shipped language. It lives here,
// not in the locale catalogues, because loading a catalogue is part of what may
// have failed.
const fallbackText: Record<string, { title: string; body: string; reload: string; inbox: string }> = {
  en: {
    title: 'Something went wrong',
    body: 'This page stopped responding. Your mail is unaffected. Reload to continue, or go back to the inbox.',
    reload: 'Reload',
    inbox: 'Back to inbox',
  },
  tr: {
    title: 'Bir şeyler ters gitti',
    body: 'Bu sayfa yanıt vermeyi durdurdu. Postanız etkilenmedi. Devam etmek için sayfayı yenileyin veya gelen kutusuna dönün.',
    reload: 'Yenile',
    inbox: 'Gelen kutusuna dön',
  },
}

// ErrorBoundary catches a rendering exception anywhere below it and shows a
// recoverable screen instead of the blank page React leaves behind when it
// unmounts the tree. It sits above every provider, so it deliberately uses no
// hooks and no context: the thing that just failed may be the very machinery a
// fancier fallback would depend on. Its text comes from fallbackText, in the
// language the language cookie or the browser names.
//
// The error text is logged, never rendered. A message can quote whatever the
// component was working on, which in a mail client is somebody's mail.
export class ErrorBoundary extends Component<Props, State> {
  state: State = { failed: false }

  static getDerivedStateFromError(): State {
    return { failed: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('render error:', describeError(error), info.componentStack)
  }

  render() {
    if (!this.state.failed) return this.props.children
    const text = fallbackText[resolveLocale(getCookie('hermex-language'), navigator.language)] ?? fallbackText.en
    return (
      <div className="min-h-screen flex items-center justify-center p-6 bg-background text-foreground">
        <div className="max-w-md space-y-4 text-center">
          <h1 className="text-xl font-semibold">{text.title}</h1>
          <p className="text-sm text-muted-foreground">{text.body}</p>
          <div className="flex justify-center gap-3">
            <button
              type="button"
              className="rounded-md bg-indigo-600 px-4 py-2 text-sm text-white"
              onClick={() => window.location.reload()}
            >
              {text.reload}
            </button>
            <button
              type="button"
              className="rounded-md border px-4 py-2 text-sm"
              onClick={() => window.location.assign('/inbox')}
            >
              {text.inbox}
            </button>
          </div>
        </div>
      </div>
    )
  }
}
