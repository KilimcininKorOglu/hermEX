import { useEffect, useRef } from "react"

// Mailbox change events the SSE server emits. The webmail reacts to all of them
// with a silent refetch (push-to-pull): the server only signals "something
// changed", the UI pulls the data over HTTP.
const MAIL_EVENTS = ["new_mail", "expunge", "flags_changed", "folder_update"] as const

type Listener = () => void

// A single shared EventSource is multiplexed to every subscriber, so opening
// the inbox plus a folder view never creates more than one connection. The
// connection is opened lazily on the first subscriber and closed when the last
// one unsubscribes (ref-counted).
const listeners = new Set<Listener>()
let source: EventSource | null = null
let coalesceTimer: ReturnType<typeof setTimeout> | null = null

// dispatch coalesces bursts (e.g. a bulk flag change) into one notification per
// ~300ms so a flurry of events triggers a single refetch, keeping traffic low.
function dispatch() {
  if (coalesceTimer) return
  coalesceTimer = setTimeout(() => {
    coalesceTimer = null
    listeners.forEach((l) => {
      try {
        l()
      } catch {
        /* a failing listener must not break the others */
      }
    })
  }, 300)
}

function openConnection() {
  if (source) return
  try {
    // /api/v1/events authenticates via the same-origin HttpOnly jwt cookie, so
    // EventSource is the correct client; the browser auto-reconnects on drops.
    source = new EventSource("/api/v1/events", { withCredentials: true })
    for (const name of MAIL_EVENTS) source.addEventListener(name, dispatch)
  } catch {
    source = null
  }
}

function closeConnectionIfIdle() {
  if (listeners.size > 0 || !source) return
  source.close()
  source = null
  if (coalesceTimer) {
    clearTimeout(coalesceTimer)
    coalesceTimer = null
  }
}

// subscribeMailEvents registers a listener and returns an unsubscribe function.
export function subscribeMailEvents(listener: Listener): () => void {
  listeners.add(listener)
  openConnection()
  return () => {
    listeners.delete(listener)
    closeConnectionIfIdle()
  }
}

// useMailEvents calls onChange whenever a mailbox change is pushed over SSE.
// The latest onChange is kept in a ref so passing a fresh inline callback each
// render does not churn the subscription.
export function useMailEvents(onChange: () => void) {
  const ref = useRef(onChange)
  useEffect(() => {
    ref.current = onChange
  })
  useEffect(() => subscribeMailEvents(() => ref.current()), [])
}
