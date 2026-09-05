import { describe, it, expect } from 'vitest'
import { dragSet, emptySelection, selectOnClick } from './attachmentSelection'

describe('selectOnClick', () => {
  // A plain click still opens the attachment, so selecting must not swallow it.
  it('reports a plain click as not a selection', () => {
    const { selection, selected } = selectOnClick(emptySelection, 1, {})
    expect(selected).toBe(false)
    expect(selection).toBe(emptySelection)
  })

  it('adds and removes with ctrl', () => {
    let s = selectOnClick(emptySelection, 2, { ctrlKey: true }).selection
    s = selectOnClick(s, 0, { ctrlKey: true }).selection
    expect(s.indexes).toEqual([0, 2])
    s = selectOnClick(s, 2, { ctrlKey: true }).selection
    expect(s.indexes).toEqual([0])
  })

  it('extends from the anchor with shift, in either direction', () => {
    const anchored = selectOnClick(emptySelection, 2, { ctrlKey: true }).selection
    expect(selectOnClick(anchored, 4, { shiftKey: true }).selection.indexes).toEqual([2, 3, 4])
    expect(selectOnClick(anchored, 0, { shiftKey: true }).selection.indexes).toEqual([0, 1, 2])
  })

  // Shift with nothing selected has no anchor to extend from, so it opens the
  // attachment rather than selecting an arbitrary range.
  it('treats shift with no anchor as a plain click', () => {
    expect(selectOnClick(emptySelection, 3, { shiftKey: true }).selected).toBe(false)
  })
})

describe('dragSet', () => {
  // Explorer's semantics: a gesture inside the selection covers all of it, one
  // outside covers only that item.
  it('covers the selection when the gesture starts inside it', () => {
    const s = { indexes: [0, 2], anchor: 0 }
    expect(dragSet(s, 2)).toEqual([0, 2])
    expect(dragSet(s, 1)).toEqual([1])
    expect(dragSet(emptySelection, 1)).toEqual([1])
  })
})
