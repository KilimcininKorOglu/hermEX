import type { Gate } from "@/utils/singleFlight"

// DraftSession coordinates the draft saves of one composer with its send. A save
// that answers after the send, or one the send overtook, would otherwise store a
// draft of a message already sent, and a send that read the draft id before the
// first save answered would leave that draft behind.
export interface DraftSession {
  // save stores the draft through the gate unless a send has begun. store is given
  // the current draft id and returns the id the server answered with.
  save(store: (id: string | undefined) => Promise<string | undefined>): Promise<boolean>
  // settle stops every later save, waits for a running one, and returns the draft
  // id the send must consume.
  settle(): Promise<string | undefined>
  // reopen lets saving resume after a send that failed.
  reopen(): void
}

export function draftSession(gate: Gate): DraftSession {
  let id: string | undefined
  let inFlight: Promise<boolean> | null = null
  let closed = false
  return {
    save(store) {
      if (closed) return Promise.resolve(false)
      const run = gate.run(async () => {
        const answered = await store(id)
        if (answered) id = answered
      })
      inFlight = run
      return run
    },
    async settle() {
      closed = true
      await inFlight
      return id
    },
    reopen() {
      closed = false
    },
  }
}
