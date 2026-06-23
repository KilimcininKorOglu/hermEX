import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import API from './api'

// The Notes API is backed by IPM.StickyNote messages in the shared "Notes"
// folder, so these endpoints are the webmail surface over the same notes EWS
// and IMAP see. The tests assert the client builds the right requests; the
// cross-protocol behavior itself is covered by helper-projects/proto_notes.py.
describe('Notes API client', () => {
  beforeEach(() => {
    API.setToken(null)
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('getNotes issues a GET to /notes', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      headers: new Headers({ 'content-type': 'application/json' }),
      json: async () => ({ notes: [{ id: 'a', title: 'T', body: 'B' }] }),
    })

    const res = await API.getNotes()

    expect(res.notes?.[0].title).toBe('T')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      expect.stringContaining('/notes'),
      expect.objectContaining({ method: 'GET' })
    )
  })

  it('createNote POSTs the title and body to /notes', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 201,
      headers: new Headers({ 'content-type': 'application/json' }),
      json: async () => ({ id: 'new', title: 'Hello', body: 'World' }),
    })

    await API.createNote({ title: 'Hello', body: 'World' })

    expect(globalThis.fetch).toHaveBeenCalledWith(
      expect.stringContaining('/notes'),
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ title: 'Hello', body: 'World' }),
      })
    )
  })

  it('updateNote PUTs to /notes/{id} with the id encoded', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      headers: new Headers({ 'content-type': 'application/json' }),
      json: async () => ({ id: 'x', title: 'New', body: 'Body' }),
    })

    await API.updateNote('a b/c', { title: 'New', body: 'Body' })

    expect(globalThis.fetch).toHaveBeenCalledWith(
      expect.stringContaining('/notes/a%20b%2Fc'),
      expect.objectContaining({ method: 'PUT' })
    )
  })

  it('deleteNote DELETEs /notes/{id}', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      headers: new Headers(),
      text: async () => '',
    })

    await API.deleteNote('note-1')

    expect(globalThis.fetch).toHaveBeenCalledWith(
      expect.stringContaining('/notes/note-1'),
      expect.objectContaining({ method: 'DELETE' })
    )
  })
})
