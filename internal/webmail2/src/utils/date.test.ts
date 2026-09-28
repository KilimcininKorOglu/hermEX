import { describe, it, expect, afterEach } from 'vitest'
import { formatAbsolute, formatDate, formatFullDate, setDisplayTimeZone, withTz, zonedInputToISO } from './date'

describe('without a chosen time zone', () => {
  afterEach(() => setDisplayTimeZone(''))

  it('renders in UTC and labels a full date as UTC', () => {
    setDisplayTimeZone('')
    expect(withTz().timeZone).toBe('UTC')
    const at = '2026-03-28T12:33:00Z'
    expect(formatFullDate(at).endsWith(' UTC')).toBe(true)
    expect(formatAbsolute(at)).toContain('12:33')
    expect(zonedInputToISO('2026-03-28T15:33')).toBe('2026-03-28T15:33:00.000Z')
  })

  it('renders in the chosen zone without a label once one is set', () => {
    setDisplayTimeZone('Europe/Istanbul')
    const at = '2026-03-28T12:33:00Z'
    expect(formatAbsolute(at)).toMatch(/0?3:33|15:33/)
    expect(formatAbsolute(at).endsWith('UTC')).toBe(false)
    expect(zonedInputToISO('2026-03-28T15:33')).toBe('2026-03-28T12:33:00.000Z')
  })
})

describe('formatDate', () => {
  it('returns time for dates less than 24 hours old', () => {
    const now = new Date()
    const oneHourAgo = new Date(now.getTime() - 3600000).toISOString()
    const result = formatDate(oneHourAgo)
    // Should return a time string with colon
    expect(result).toMatch(/^\d{1,2}:\d{2}/)
  })

  it('returns short string for dates less than 7 days old', () => {
    const now = new Date()
    const twoDaysAgo = new Date(now.getTime() - 2 * 86400000).toISOString()
    const result = formatDate(twoDaysAgo)
    // Should return a short string (weekday name)
    expect(result.length).toBeLessThanOrEqual(4)
  })

  it('returns month and day for dates older than 7 days', () => {
    const tenDaysAgo = new Date(Date.now() - 10 * 86400000).toISOString()
    const result = formatDate(tenDaysAgo)
    // Should contain a space and a number (some locale format)
    expect(result).toMatch(/\s/)
    expect(result).toMatch(/\d/)
  })
})

describe('formatFullDate', () => {
  it('returns full date and time string', () => {
    const dateStr = '2024-04-15T14:30:00Z'
    const result = formatFullDate(dateStr)
    // Should contain the year
    expect(result).toContain('2024')
  })

  it('handles dates correctly', () => {
    const date = '2024-01-01T12:00:00Z' // Jan 1, 2024 in UTC, the zone shown when none is chosen
    const result = formatFullDate(date)
    // Should contain year and day
    expect(result).toContain('2024')
    expect(result).toContain('1')
  })
})
