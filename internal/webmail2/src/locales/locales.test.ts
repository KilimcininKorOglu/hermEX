import { describe, it, expect } from 'vitest'
import en from './en.json'
import tr from './tr.json'

type Catalogue = Record<string, unknown>

// leaves flattens a catalogue into its dotted keys and their values.
function leaves(o: Catalogue, prefix = ''): Map<string, unknown> {
  const out = new Map<string, unknown>()
  for (const [k, v] of Object.entries(o)) {
    if (v && typeof v === 'object') {
      for (const [kk, vv] of leaves(v as Catalogue, prefix + k + '.')) out.set(kk, vv)
    } else {
      out.set(prefix + k, v)
    }
  }
  return out
}

// sources are the SPA's own modules as text, the tests excluded.
const sources = import.meta.glob(['../**/*.{ts,tsx}', '!../**/*.test.{ts,tsx}'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

// usedKeys are the literal keys the code passes to t().
function usedKeys(): Map<string, string> {
  const out = new Map<string, string>()
  for (const [file, text] of Object.entries(sources)) {
    for (const m of text.matchAll(/\bt\(\s*["']([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)+)["']/g)) {
      out.set(m[1], file)
    }
  }
  return out
}

describe('locale catalogues', () => {
  const enKeys = leaves(en as Catalogue)
  const trKeys = leaves(tr as Catalogue)

  it('carry the same keys in every language', () => {
    expect([...trKeys.keys()].filter((k) => !enKeys.has(k))).toEqual([])
    expect([...enKeys.keys()].filter((k) => !trKeys.has(k))).toEqual([])
  })

  it('hold a non-empty string for every key', () => {
    for (const [lang, keys] of [['en', enKeys], ['tr', trKeys]] as const) {
      const bad = [...keys].filter(([, v]) => typeof v !== 'string' || v.trim() === '').map(([k]) => k)
      expect(bad, lang).toEqual([])
    }
  })

  it('define every key the code asks for', () => {
    const missing = [...usedKeys()].filter(([k]) => !enKeys.has(k)).map(([k, file]) => `${k} (${file})`)
    expect(missing).toEqual([])
  })
})
