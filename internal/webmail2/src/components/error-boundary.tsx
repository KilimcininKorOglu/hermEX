import { Component, type ErrorInfo, type ReactNode } from 'react'
import { describeError } from '@/utils/errorlog'

interface Props {
  children: ReactNode
}

interface State {
  failed: boolean
}

// ErrorBoundary catches a rendering exception anywhere below it and shows a
// recoverable screen instead of the blank page React leaves behind when it
// unmounts the tree. It sits above every provider, so it deliberately uses no
// hooks, no context and no translations: the thing that just failed may be the
// very machinery a fancier fallback would depend on.
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
    return (
      <div className="min-h-screen flex items-center justify-center p-6 bg-background text-foreground">
        <div className="max-w-md space-y-4 text-center">
          <h1 className="text-xl font-semibold">Something went wrong</h1>
          <p className="text-sm text-muted-foreground">
            This page stopped responding. Your mail is unaffected. Reload to continue, or go
            back to the inbox.
          </p>
          <div className="flex justify-center gap-3">
            <button
              type="button"
              className="rounded-md bg-indigo-600 px-4 py-2 text-sm text-white"
              onClick={() => window.location.reload()}
            >
              Reload
            </button>
            <button
              type="button"
              className="rounded-md border px-4 py-2 text-sm"
              onClick={() => window.location.assign('/inbox')}
            >
              Back to inbox
            </button>
          </div>
        </div>
      </div>
    )
  }
}

export default ErrorBoundary
