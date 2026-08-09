// Client-side error reporting. The SPA has no server-side log path of its own,
// so the console is the only place a browser-side failure can land; what matters
// is that it lands there at all rather than disappearing with the page.

// describeError renders any thrown value as one line. A rejection can carry
// anything, not just an Error, and "[object Object]" in the console is worth
// nothing to whoever is reading it.
export function describeError(value: unknown): string {
  if (value instanceof Error) {
    return value.stack || `${value.name}: ${value.message}`
  }
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

// installGlobalErrorHandlers records uncaught exceptions and unhandled promise
// rejections. Neither reaches an error boundary: a boundary only sees errors
// thrown during render, so an async failure (a rejected fetch in an effect, an
// event handler that throws) would otherwise leave no trace at all. It returns
// a function that removes the listeners, which the tests use and a hot reload
// would want.
export function installGlobalErrorHandlers(target: Window = window): () => void {
  const onError = (e: ErrorEvent) => {
    console.error('uncaught error:', describeError(e.error ?? e.message))
  }
  const onRejection = (e: PromiseRejectionEvent) => {
    console.error('unhandled promise rejection:', describeError(e.reason))
  }
  target.addEventListener('error', onError)
  target.addEventListener('unhandledrejection', onRejection)
  return () => {
    target.removeEventListener('error', onError)
    target.removeEventListener('unhandledrejection', onRejection)
  }
}
